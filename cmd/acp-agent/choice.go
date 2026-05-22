package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type choiceOption struct {
	Key   string
	Label string
}

func (c *client) choose(title, detail string, options []choiceOption) (string, error) {
	ctx := c.activeRequestContext()
	if c.events != nil {
		reply := make(chan string, 1)
		choiceCtx, cancel := context.WithCancel(ctx)
		c.setChoiceCancel(cancel)
		defer func() {
			cancel()
			c.clearChoiceCancel(cancel)
		}()
		c.emit(uiPermissionRequest{Title: title, Detail: detail, Options: options, Reply: reply})
		select {
		case choice := <-reply:
			return normalizeChoice(choice, options), nil
		case <-choiceCtx.Done():
			return "", choiceCtx.Err()
		case <-c.done:
			return "", errors.New("connection closed")
		}
	}
	fmt.Fprintf(c.stderr, "\n%s\n", title)
	if strings.TrimSpace(detail) != "" {
		fmt.Fprintln(c.stderr, detail)
	}
	for i, opt := range options {
		key := opt.Key
		if key == "" {
			key = strconv.Itoa(i + 1)
		}
		fmt.Fprintf(c.stderr, "%s: %s\n", key, opt.Label)
	}
	fmt.Fprint(c.stderr, "select: ")
	line, err := c.stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return normalizeChoice(line, options), nil
}

func (c *client) setChoiceCancel(cancel context.CancelFunc) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.choiceCancel = cancel
	c.mu.Unlock()
}

func (c *client) clearChoiceCancel(cancel context.CancelFunc) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.choiceCancel = nil
	c.mu.Unlock()
}

func normalizeChoice(line string, options []choiceOption) string {
	choice := strings.ToLower(strings.TrimSpace(line))
	for i, opt := range options {
		key := strings.ToLower(strings.TrimSpace(opt.Key))
		if key == "" {
			key = strconv.Itoa(i + 1)
		}
		if choice == key {
			return key
		}
	}
	return choice
}
