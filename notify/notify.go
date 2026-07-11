// Package notify sends notifications out of the process to a human.
//
// The interface is the point. Callers hold a Notifier and describe *what happened*; they
// never learn how it gets delivered. ntfy is today's transport (ntfy.go) — a mail or Slack
// sender is a new type in this package and not one line changed anywhere else.
package notify

import (
	"context"
	"fmt"
)

// Priority maps onto ntfy's 1..5. Default is the zero value, so a Notification that never
// mentions priority still sends correctly.
type Priority int

const (
	Default Priority = iota
	Min
	Low
	High
	Max
)

// ntfyLevel converts to the 1..5 an ntfy server expects. Kept here (not in ntfy.go) only
// because it is the one place the mapping is defined.
func (p Priority) ntfyLevel() int {
	switch p {
	case Min:
		return 1
	case Low:
		return 2
	case High:
		return 4
	case Max:
		return 5
	default:
		return 3
	}
}

// A Notification is what happened, not how it is delivered. A transport that has no notion
// of Tags or Click is free to ignore them.
type Notification struct {
	Title string
	Body  string
	Tags  []string // ntfy renders known names as emoji: "eyes", "warning", "skull"
	Click string   // URL opened when the notification is tapped
	Priority
}

// Notifier delivers a Notification. One method, because that is all any caller needs —
// a wide interface would force every future transport to implement things nobody calls.
//
// Notify must respect ctx, and its error is informational: nothing in this project fails a
// request because a notification failed.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Nop discards everything. It exists so an unconfigured notifier is still a *usable*
// Notifier: callers hold a Nop instead of a nil, and no call site needs a nil check.
type Nop struct{}

func (Nop) Notify(context.Context, Notification) error { return nil }

func (Nop) String() string { return "off" }

// Multi sends to every Notifier and reports the first failure, after trying them all —
// one dead transport must not silence the others.
type Multi []Notifier

func (m Multi) Notify(ctx context.Context, n Notification) error {
	var firstErr error
	for _, to := range m {
		if err := to.Notify(ctx, n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m Multi) String() string { return fmt.Sprint([]Notifier(m)) }
