package notify

import (
	"errors"
	"testing"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want Toast
	}{
		{
			name: "repo present (title includes repo, subtitle is session)",
			in: Input{
				PlanName: "add-billing",
				Repo:     "afk",
				Message:  "Build complete",
			},
			want: Toast{
				Title:    "afk · afk",
				Subtitle: "add-billing",
				Body:     "Build complete",
				Group:    "afk-afk-add-billing",
			},
		},
		{
			name: "repo empty (title is 'afk' exactly, subtitle is session)",
			in: Input{
				PlanName: "add-billing",
				Repo:     "",
				Message:  "Build complete",
			},
			want: Toast{
				Title:    "afk",
				Subtitle: "add-billing",
				Body:     "Build complete",
				Group:    "afk-add-billing",
			},
		},
		{
			name: "same plan name with two different repos (distinct groups)",
			in: Input{
				PlanName: "refactor",
				Repo:     "web-app",
				Message:  "Ready",
			},
			want: Toast{
				Title:    "afk · web-app",
				Subtitle: "refactor",
				Body:     "Ready",
				Group:    "afk-web-app-refactor",
			},
		},
		{
			name: "same plan name, different repo B",
			in: Input{
				PlanName: "refactor",
				Repo:     "api-server",
				Message:  "Ready",
			},
			want: Toast{
				Title:    "afk · api-server",
				Subtitle: "refactor",
				Body:     "Ready",
				Group:    "afk-api-server-refactor",
			},
		},
		{
			name: "empty plan name (fallback to 'afk session' in subtitle)",
			in: Input{
				PlanName: "",
				Repo:     "my-repo",
				Message:  "Status update",
			},
			want: Toast{
				Title:    "afk · my-repo",
				Subtitle: "afk session",
				Body:     "Status update",
				Group:    "afk-my-repo-afk session",
			},
		},
		{
			name: "empty message (fallback to default message)",
			in: Input{
				PlanName: "deploy",
				Repo:     "backend",
				Message:  "",
			},
			want: Toast{
				Title:    "afk · backend",
				Subtitle: "deploy",
				Body:     "Claude needs your attention",
				Group:    "afk-backend-deploy",
			},
		},
		{
			name: "message with double quote (verbatim passthrough)",
			in: Input{
				PlanName: "test",
				Repo:     "repo",
				Message:  `Value is "important"`,
			},
			want: Toast{
				Title:    "afk · repo",
				Subtitle: "test",
				Body:     `Value is "important"`,
				Group:    "afk-repo-test",
			},
		},
		{
			name: "message with backslash (verbatim passthrough)",
			in: Input{
				PlanName: "test",
				Repo:     "repo",
				Message:  `Path is C:\Windows`,
			},
			want: Toast{
				Title:    "afk · repo",
				Subtitle: "test",
				Body:     `Path is C:\Windows`,
				Group:    "afk-repo-test",
			},
		},
		{
			name: "message with newline (verbatim passthrough)",
			in: Input{
				PlanName: "test",
				Repo:     "repo",
				Message:  "Line 1\nLine 2",
			},
			want: Toast{
				Title:    "afk · repo",
				Subtitle: "test",
				Body:     "Line 1\nLine 2",
				Group:    "afk-repo-test",
			},
		},
		{
			name: "message with leading dash (verbatim passthrough)",
			in: Input{
				PlanName: "test",
				Repo:     "repo",
				Message:  "-flag value",
			},
			want: Toast{
				Title:    "afk · repo",
				Subtitle: "test",
				Body:     "-flag value",
				Group:    "afk-repo-test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Format(tt.in)
			if got.Title != tt.want.Title {
				t.Errorf("Title = %q, want %q", got.Title, tt.want.Title)
			}
			if got.Subtitle != tt.want.Subtitle {
				t.Errorf("Subtitle = %q, want %q", got.Subtitle, tt.want.Subtitle)
			}
			if got.Body != tt.want.Body {
				t.Errorf("Body = %q, want %q", got.Body, tt.want.Body)
			}
			if got.Group != tt.want.Group {
				t.Errorf("Group = %q, want %q", got.Group, tt.want.Group)
			}
			if got.Sound != "" {
				t.Errorf("Sound = %q, want empty (caller sets it)", got.Sound)
			}
		})
	}
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name        string
		available   map[string]bool
		wantBackend Backend
		wantPath    string
		wantFound   bool
	}{
		{
			name: "alerter present",
			available: map[string]bool{
				"alerter": true,
			},
			wantBackend: BackendAlerter,
			wantPath:    "/usr/local/bin/alerter",
			wantFound:   true,
		},
		{
			name: "alerter absent, osascript present",
			available: map[string]bool{
				"osascript": true,
			},
			wantBackend: BackendOsascript,
			wantPath:    "/usr/bin/osascript",
			wantFound:   true,
		},
		{
			name:        "both absent",
			available:   map[string]bool{},
			wantBackend: "",
			wantPath:    "",
			wantFound:   false,
		},
		{
			name: "alerter wins when both exist",
			available: map[string]bool{
				"alerter":   true,
				"osascript": true,
			},
			wantBackend: BackendAlerter,
			wantPath:    "/usr/local/bin/alerter",
			wantFound:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookPath := func(name string) (string, error) {
				if tt.available[name] {
					if name == "alerter" {
						return "/usr/local/bin/alerter", nil
					}
					if name == "osascript" {
						return "/usr/bin/osascript", nil
					}
				}
				return "", errors.New("not found")
			}

			backend, path, found := Select(lookPath)
			if backend != tt.wantBackend {
				t.Errorf("backend = %v, want %v", backend, tt.wantBackend)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}
		})
	}
}
