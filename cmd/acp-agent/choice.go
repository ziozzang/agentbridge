package main

import (
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
	if c.events != nil {
		reply := make(chan string, 1)
		c.emit(uiPermissionRequest{Title: title, Detail: detail, Options: options, Reply: reply})
		choice := <-reply
		return normalizeChoice(choice, options), nil
	}
	if c.ui != nil && c.ui.active() {
		c.ui.overlay(title, detail, options)
	} else if c.ui != nil {
		c.ui.clear()
	}
	if c.ui == nil || !c.ui.active() {
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
	}
	fmt.Fprint(c.stderr, "select: ")
	line, err := c.stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return normalizeChoice(line, options), nil
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
