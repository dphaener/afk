package webpush

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DevicesFileName is the list of signed-up devices, beside push-key.json.
const DevicesFileName = "push-devices.json"

// Device is one signed-up browser or phone: the subscription it handed out
// plus a name the owner can recognise and when it signed up. The keys are
// that device's secret material, so the file is mode 0600.
type Device struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
	Label    string `json:"label"`
	At       string `json:"at"`
}

// Subscription is the part of a Device the sender needs.
func (d Device) Subscription() Subscription {
	return Subscription{Endpoint: d.Endpoint, P256dh: d.P256dh, Auth: d.Auth}
}

// devicesFile is the file shape. The list is always present, never null, so
// a reader in another language never has to special-case an empty file.
type devicesFile struct {
	Devices []Device `json:"devices"`
}

// deviceMu serialises the read-modify-write of the list within one process
// (the daemon and the page share one). Writes are re-read first so a second
// process's change is not lost either, short of a true simultaneous write.
var deviceMu sync.Mutex

// ReadDevices returns the signed-up devices. A missing or damaged file reads
// as an empty list, never nil and never an error: a device list that cannot
// be read must not stop the build loop or the page, and the next AddDevice
// replaces a damaged file (the same rule as daemonstate.ReadPlanWaits).
func ReadDevices(dir string) []Device {
	data, err := os.ReadFile(filepath.Join(dir, DevicesFileName))
	if err != nil {
		return []Device{}
	}
	var f devicesFile
	if json.Unmarshal(data, &f) != nil || f.Devices == nil {
		return []Device{}
	}
	return f.Devices
}

func writeDevices(dir string, devices []Device) error {
	if devices == nil {
		devices = []Device{}
	}
	out, err := json.MarshalIndent(devicesFile{Devices: devices}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the push device list: %w", err)
	}
	return writeFileAtomic(dir, DevicesFileName, append(out, '\n'))
}

// validEndpoint reports whether an endpoint may be kept. https is the rule:
// push services are https. http is allowed only for a loopback host so tests
// against httptest.NewServer work; no real push service is on loopback, and a
// loopback address cannot be used to reach anything but this machine.
func validEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return false
}

// AddDevice keeps a subscription. An endpoint already in the list replaces
// that entry's keys, label and time and keeps its id: a browser that
// subscribes again (new keys after a reinstall) is the same device, and a
// second row would send it every notice twice. A new endpoint gets a random
// 16-byte hex id from rnd. Errors never quote the endpoint, which is the
// device's capability URL.
func AddDevice(dir string, sub Subscription, label string, now time.Time, rnd io.Reader) (Device, error) {
	if !validEndpoint(sub.Endpoint) {
		return Device{}, errors.New("the push endpoint is not an absolute https address")
	}
	if sub.P256dh == "" || sub.Auth == "" {
		return Device{}, errors.New("the push subscription is missing its keys")
	}
	if rnd == nil {
		rnd = rand.Reader
	}
	deviceMu.Lock()
	defer deviceMu.Unlock()
	devices := ReadDevices(dir)
	dev := Device{
		Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth,
		Label: label, At: now.UTC().Format(time.RFC3339),
	}
	found := false
	for i := range devices {
		if devices[i].Endpoint == sub.Endpoint {
			dev.ID = devices[i].ID
			devices[i] = dev
			found = true
			break
		}
	}
	if !found {
		var raw [16]byte
		if _, err := io.ReadFull(rnd, raw[:]); err != nil {
			return Device{}, errors.New("making a device id failed: no random bytes")
		}
		dev.ID = hex.EncodeToString(raw[:])
		devices = append(devices, dev)
	}
	if err := writeDevices(dir, devices); err != nil {
		return Device{}, err
	}
	return dev, nil
}

// RemoveDevice drops the device with this id. Removing what is not there is
// (false, nil): the owner's tap and the sender's gone answer can race.
func RemoveDevice(dir, id string) (bool, error) {
	return removeWhere(dir, func(d Device) bool { return d.ID == id })
}

// RemoveEndpoint drops the device with this endpoint, for a push service that
// answered that the subscription is gone.
func RemoveEndpoint(dir, endpoint string) (bool, error) {
	return removeWhere(dir, func(d Device) bool { return d.Endpoint == endpoint })
}

func removeWhere(dir string, match func(Device) bool) (bool, error) {
	deviceMu.Lock()
	defer deviceMu.Unlock()
	devices := ReadDevices(dir)
	kept := make([]Device, 0, len(devices))
	for _, d := range devices {
		if !match(d) {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(devices) {
		return false, nil
	}
	if err := writeDevices(dir, kept); err != nil {
		return false, err
	}
	return true, nil
}

// Label names the browser and system from a User-Agent, for the list the
// owner sees ("Chrome on Android"). It is a guess from substrings and the
// order matters: Edge and Chrome user agents also contain "Safari", Chrome
// ones contain "Chrome" and Android ones "Linux", and iPhone and iPad ones
// contain "Mac OS X". So the system is checked iPhone, iPad, Android, Windows,
// Mac, Linux, and the browser Edge, Firefox, Chrome, Safari. The known limit:
// iPadOS Safari sends a Mac user agent ("Macintosh") by default, so it reads
// as "Safari on Mac". Anything unrecognised is "Unknown browser".
func Label(userAgent string) string {
	ua := userAgent
	var browser string
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "EdgA/") || strings.Contains(ua, "EdgiOS/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	default:
		return "Unknown browser"
	}
	var system string
	switch {
	case strings.Contains(ua, "iPhone"):
		system = "iPhone"
	case strings.Contains(ua, "iPad"):
		system = "iPad"
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		system = "Mac"
	case strings.Contains(ua, "Linux") || strings.Contains(ua, "X11"):
		system = "Linux"
	default:
		return browser
	}
	return browser + " on " + system
}
