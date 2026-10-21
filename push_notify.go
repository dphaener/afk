package main

import (
	"context"
	"time"

	"github.com/dphaener/afk/internal/webpush"
)

// pushSendTimeout bounds one device's push. Each device gets its own clock so
// one slow push service cannot use up the time of the devices after it; the
// daemon's pass waits on this loop, so the bound also caps how long a dead
// service can hold the pass (webpush.Sender has its own 10 second client
// timeout, this one covers the key load and everything around it).
const pushSendTimeout = 10 * time.Second

// pushSendFunc has the shape of web.Deps.PushSend and webPushSend: gone is
// true when the push service says the subscription is gone for good.
type pushSendFunc func(ctx context.Context, sub webpush.Subscription, p webpush.Payload, o webpush.Options) (gone bool, err error)

// pushTarget is where a send site's web push goes: the state folder holding
// push-devices.json and the function that sends. The zero value sends
// nothing, so a site with push turned off (or a state folder that could not
// be found) needs no special case.
type pushTarget struct {
	dir  string
	send pushSendFunc
}

// deliver sends one notice to every signed-up device and returns how many the
// push service took. It never fails the caller: a push is a courtesy beside
// the item or the wait, so every problem is a log line and a smaller count.
//
// The rule for a device that fails is T15.4's: only "gone" (the service said
// 404 or 410) removes it, because that is the one answer that means the
// subscription will never work again. Any other error, a 429, a 5xx or a
// network drop, keeps it, since it may work next time.
//
// Log lines name the device by its label and never hold the endpoint, p256dh
// or auth: the endpoint is the device's capability URL and logs get shared.
// logf may be nil.
func (t pushTarget) deliver(p webpush.Payload, o webpush.Options, logf func(string, ...any)) (sent int) {
	if t.dir == "" || t.send == nil {
		return 0
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := p.Validate(); err != nil {
		logf("push not sent: %v", err)
		return 0
	}
	for _, dev := range webpush.ReadDevices(t.dir) {
		ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
		gone, err := t.send(ctx, dev.Subscription(), p, o)
		cancel()
		switch {
		case err != nil:
			logf("push to %q failed, device kept: %v", dev.Label, err)
		case gone:
			if _, rerr := webpush.RemoveEndpoint(t.dir, dev.Endpoint); rerr != nil {
				logf("push device %q is gone but could not be removed from the list: %v", dev.Label, rerr)
			} else {
				logf("push device %q is gone and was removed from the list", dev.Label)
			}
		default:
			sent++
		}
	}
	return sent
}
