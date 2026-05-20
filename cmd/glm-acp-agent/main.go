// glm-acp-agent is the ACP-protocol entry point that bridges Z.AI / Zhipu AI
// GLM models into any ACP-aware client (e.g. Zed, the ACP CLI). It speaks
// JSON-RPC 2.0 over newline-delimited JSON on stdio.
//
// Environment variables (all optional unless noted):
//
//	Z_AI_API_KEY              REQUIRED for any chat. Or use --setup to store one.
//	ACP_GLM_MODEL             Default model id (e.g. glm-5.1).
//	ACP_GLM_AVAILABLE_MODELS  Comma-separated whitelist advertised to the client.
//	ACP_GLM_BASE_URL          Override Z.AI Coding Plan base URL.
//	ACP_GLM_MAX_TOKENS        Per-call max output tokens (default 8192).
//	ACP_GLM_THINKING          Force GLM thinking mode on/off (true/false).
//	ACP_GLM_DEBUG             true|1 enables verbose stderr debug logging.
//	ACP_GLM_SESSION_DIR       Directory for persisted session JSON files.
//	XDG_CONFIG_HOME           Used to locate the credentials file.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ziozzang/glm-acp/internal/acp"
	"github.com/ziozzang/glm-acp/internal/agent"
	"github.com/ziozzang/glm-acp/internal/credentials"
	"github.com/ziozzang/glm-acp/internal/grpccompat"
	"github.com/ziozzang/glm-acp/internal/httpcompat"
	"github.com/ziozzang/glm-acp/internal/logger"
	"github.com/ziozzang/glm-acp/internal/protocol/sessionstore"
)

const usage = `glm-acp-agent — ACP coding agent backed by Z.AI / Zhipu AI GLM models.

Usage:
  glm-acp-agent              # speak ACP over stdio (the default mode for IDEs)
  glm-acp-agent --server     # speak ACP over TCP, one JSON-RPC stream per connection
  glm-acp-agent --setup      # interactively store a Z.AI API key
  glm-acp-agent --help       # show this help

Server flags:
  --listen ADDR              TCP listen address (default "127.0.0.1:8765")
  --pool-size N              max concurrent TCP ACP connections (default 4)
  --wait-size N              max queued TCP ACP connections (default pool-size/2)
  --http-listen ADDR         optional HTTP compatibility API listen address
  --grpc-listen ADDR         optional gRPC compatibility API listen address

Environment:
  Z_AI_API_KEY               (required for chat) Z.AI Coding Plan API key
  ACP_GLM_MODEL              default model id (e.g. glm-5.1)
  ACP_GLM_AVAILABLE_MODELS   comma-separated whitelist advertised to clients
  ACP_GLM_BASE_URL           override the Z.AI Coding Plan base URL
  ACP_GLM_MAX_TOKENS         per-call max output tokens (default 8192)
  ACP_GLM_THINKING           force GLM thinking mode on/off (true/false)
  ACP_GLM_DEBUG              true|1 enables verbose stderr debug logging
  ACP_GLM_SESSION_DIR        directory for persisted session JSON files
  XDG_CONFIG_HOME            used to locate the credentials file
`

func main() {
	helpFlag := flag.Bool("help", false, "show help")
	hFlag := flag.Bool("h", false, "show help")
	setupFlag := flag.Bool("setup", false, "interactively store an API key")
	versionFlag := flag.Bool("version", false, "print agent version and exit")
	serverFlag := flag.Bool("server", false, "run a TCP ACP server")
	listenFlag := flag.String("listen", "127.0.0.1:8765", "TCP listen address for --server")
	poolSizeFlag := flag.Int("pool-size", 4, "max concurrent TCP ACP connections for --server")
	waitSizeFlag := flag.Int("wait-size", -1, "max queued TCP ACP connections for --server; default is pool-size/2")
	httpListenFlag := flag.String("http-listen", "", "HTTP compatibility API listen address")
	grpcListenFlag := flag.String("grpc-listen", "", "gRPC compatibility API listen address")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	if *helpFlag || *hFlag {
		fmt.Fprint(os.Stdout, usage)
		return
	}
	if *versionFlag {
		fmt.Println(agent.Version)
		return
	}
	if *setupFlag {
		if err := runSetup(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "setup failed:", err)
			os.Exit(1)
		}
		return
	}
	if *serverFlag || *httpListenFlag != "" || *grpcListenFlag != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runServers(ctx, *serverFlag, *listenFlag, *poolSizeFlag, *waitSizeFlag, *httpListenFlag, *grpcListenFlag); err != nil {
			fmt.Fprintln(os.Stderr, "server terminated:", err)
			os.Exit(1)
		}
		return
	}
	if err := runStdio(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "agent terminated:", err)
		os.Exit(1)
	}
}

func runStdio(in io.Reader, out io.Writer) error {
	if err := logger.Configure(); err != nil {
		fmt.Fprintln(os.Stderr, "logger init failed:", err)
	}
	return runACP(in, out)
}

func runACP(in io.Reader, out io.Writer) error {
	a := agent.New(sessionstore.New())
	conn := acp.NewConn(in, out, a)
	a.SetConn(conn)
	return conn.Run()
}

func runServer(ctx context.Context, addr string, poolSize, waitSize int) error {
	if err := logger.Configure(); err != nil {
		fmt.Fprintln(os.Stderr, "logger init failed:", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	return serveListener(ctx, ln, poolSize, waitSize)
}

func runServers(ctx context.Context, tcpEnabled bool, tcpAddr string, poolSize, waitSize int, httpAddr, grpcAddr string) error {
	if httpAddr == "" && grpcAddr == "" {
		return runServer(ctx, tcpAddr, poolSize, waitSize)
	}
	errCh := make(chan error, 3)
	if tcpEnabled {
		go func() { errCh <- runServer(ctx, tcpAddr, poolSize, waitSize) }()
	}
	if httpAddr != "" {
		go func() { errCh <- runHTTPServer(ctx, httpAddr) }()
	}
	if grpcAddr != "" {
		go func() { errCh <- runGRPCServer(ctx, grpcAddr) }()
	}
	if !tcpEnabled && httpAddr == "" {
		return <-errCh
	}
	if !tcpEnabled && grpcAddr == "" {
		return <-errCh
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func runHTTPServer(ctx context.Context, addr string) error {
	if err := logger.Configure(); err != nil {
		fmt.Fprintln(os.Stderr, "logger init failed:", err)
	}
	srv := &http.Server{Addr: addr, Handler: httpcompat.NewHandler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func runGRPCServer(ctx context.Context, addr string) error {
	if err := logger.Configure(); err != nil {
		fmt.Fprintln(os.Stderr, "logger init failed:", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpccompat.NewServer()
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	err = srv.Serve(ln)
	if err != nil {
		return err
	}
	return nil
}

func serveListener(ctx context.Context, ln net.Listener, poolSize, waitSize int) error {
	if poolSize <= 0 {
		return fmt.Errorf("pool-size must be greater than zero")
	}
	if waitSize < 0 {
		waitSize = defaultWaitSize(poolSize)
	}
	if waitSize < 0 {
		return fmt.Errorf("wait-size must be zero or greater")
	}
	active := make(chan struct{}, poolSize)
	waiting := make(chan struct{}, waitSize)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return err
		}
		select {
		case active <- struct{}{}:
			go runTCPACPConn(c, active)
		default:
			select {
			case waiting <- struct{}{}:
				go func() {
					select {
					case active <- struct{}{}:
						<-waiting
						runTCPACPConn(c, active)
					case <-ctx.Done():
						<-waiting
						_ = c.Close()
					}
				}()
			default:
				logger.Warnf("tcp acp connection rejected: wait queue full (%d)", waitSize)
				_ = c.Close()
			}
		}
	}
}

func defaultWaitSize(poolSize int) int {
	if poolSize <= 0 {
		return 0
	}
	return poolSize / 2
}

func runTCPACPConn(c net.Conn, active chan struct{}) {
	defer func() {
		<-active
		_ = c.Close()
	}()
	if err := runACP(c, c); err != nil {
		logger.Warnf("tcp acp connection ended: %v", err)
	}
}

func runSetup(in io.Reader, out io.Writer) error {
	if err := logger.Configure(); err != nil {
		fmt.Fprintln(os.Stderr, "logger init failed:", err)
	}
	fmt.Fprintln(out, "Enter your Z.AI / Zhipu AI Coding Plan API key.")
	fmt.Fprintln(out, "It will be saved to "+credentials.Path()+" with mode 0600.")
	fmt.Fprint(out, "API key: ")
	br := bufio.NewReader(in)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	key := strings.TrimSpace(line)
	if key == "" {
		return fmt.Errorf("no key provided")
	}
	if err := credentials.Write(key, ""); err != nil {
		return err
	}
	fmt.Fprintln(out, "Saved.")
	return nil
}
