package rex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// FakeCall is one recorded call.
type FakeCall struct {
	Method string
	Args   json.RawMessage
}

type fakeAnswer struct {
	method string
	args   string // marshalled args; "" matches any args
	reply  json.RawMessage
	err    error
}

// Fake is a Caller for tests. Script answers with On/OnArgs; every call is
// recorded in order. An unscripted call returns an error naming the method so
// a test fails loudly instead of seeing an empty answer.
type Fake struct {
	mu      sync.Mutex
	answers []fakeAnswer
	calls   []FakeCall
}

// On scripts a reply (or error) for a method with any args.
func (f *Fake) On(method string, reply string, err error) *Fake {
	return f.add(method, "", reply, err)
}

// OnArgs scripts a reply for a method called with exactly these args.
func (f *Fake) OnArgs(method string, args any, reply string, err error) *Fake {
	b, merr := json.Marshal(args)
	if merr != nil {
		panic(merr)
	}
	return f.add(method, string(b), reply, err)
}

func (f *Fake) add(method, args, reply string, err error) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, fakeAnswer{method: method, args: args, reply: json.RawMessage(reply), err: err})
	return f
}

// Calls returns the recorded calls in order.
func (f *Fake) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeCall(nil), f.calls...)
}

// Call implements Caller. The first matching scripted answer wins; an
// args-specific answer is preferred over a catch-all.
func (f *Fake) Call(_ context.Context, method string, args any) (json.RawMessage, error) {
	if args == nil {
		args = struct{}{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, &CallError{Method: method, Err: err}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, FakeCall{Method: method, Args: b})
	var fallback *fakeAnswer
	for i := range f.answers {
		a := &f.answers[i]
		if a.method != method {
			continue
		}
		if a.args == string(b) {
			return a.reply, a.err
		}
		if a.args == "" && fallback == nil {
			fallback = a
		}
	}
	if fallback != nil {
		return fallback.reply, fallback.err
	}
	return nil, fmt.Errorf("rex fake: no scripted answer for method %q with args %s", method, b)
}
