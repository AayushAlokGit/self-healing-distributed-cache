// Package notify sends notifications out of the process to a human. Callers describe *what
// happened* and never learn how it is delivered: a mail or Slack sender is a new type in
// this package and not one line changed anywhere else.
package notify

import (
	"context"
	"fmt"
)

// Priority maps onto ntfy's 1..5, with Default as the zero value — a Notification that never
// mentions priority still sends correctly.
type Priority int

const (
	Default Priority = iota
	Min
	Low
	High
	Max
)

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

// A Notification is what happened. A transport with no notion of Tags or Click may ignore them.
type Notification struct {
	Title string
	Body  string
	Tags  []string // ntfy renders known names as emoji: "eyes", "warning", "skull"
	Click string   // URL opened when the notification is tapped
	Priority
}

// Notifier delivers a Notification. Notify must respect ctx, and its error is informational:
// nothing in this project fails a request because a notification failed.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Nop discards everything, so an unconfigured notifier is still a *usable* Notifier: callers
// hold a Nop rather than a nil, and no call site needs a nil check.
type Nop struct{}

func (Nop) Notify(context.Context, Notification) error { return nil }

func (Nop) String() string { return "off" }

// Multi reports the first failure only after trying them all — one dead transport must not
// silence the others.
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
