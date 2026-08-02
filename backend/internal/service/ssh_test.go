package service

import (
	"errors"
	"testing"
)

func TestIsConnectionError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"command exit", errors.New("exit status 1"), false},
		{"command not found", errors.New("command not found"), false},
		{"connection closed", errors.New("ssh: connection closed"), true},
		{"broken pipe", errors.New("read: broken pipe"), true},
		{"i/o timeout", errors.New("i/o timeout"), true},
		{"network unreachable", errors.New("network is unreachable"), true},
		{"no route to host", errors.New("no route to host"), true},
		{"connection reset", errors.New("connection reset by peer"), true},
		{"auth failure", errors.New("unable to authenticate"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isConnectionError(c.err); got != c.want {
				t.Errorf("isConnectionError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
