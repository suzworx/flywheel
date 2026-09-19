package flywheel

import (
	"strings"
	"testing"
)

func TestPlanText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  bool
		start string
	}{
		{
			name:  "plain",
			text:  "PLAN files-to-read: a.go\nPLAN order: x",
			want:  true,
			start: "PLAN files-to-read",
		},
		{
			name:  "bold",
			text:  "**PLAN files-to-read:** a.go\n**PLAN order:** x",
			want:  true,
			start: "**PLAN files-to-read",
		},
		{
			name:  "bullet",
			text:  "- PLAN files-to-read: a.go",
			want:  true,
			start: "PLAN files-to-read",
		},
		{
			name:  "heading",
			text:  "## PLAN files-to-read: a.go",
			want:  true,
			start: "PLAN files-to-read",
		},
		{
			name:  "prose first",
			text:  "I'll read the files first.\n\nPLAN files-to-read: a.go",
			want:  true,
			start: "PLAN files-to-read",
		},
		{
			name:  "indented",
			text:  "   PLAN checks: go test",
			want:  true,
			start: "PLAN checks",
		},
		{
			name: "mention only",
			text: "My plan is to read the files.",
			want: false,
		},
		{
			name: "lowercase",
			text: "plan files-to-read: a.go",
			want: false,
		},
		{
			name: "word inside a line",
			text: "The PLAN files are here",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, isPlan := planText(tt.text)
			if isPlan != tt.want {
				t.Errorf("isPlan = %v, want %v", isPlan, tt.want)
			}
			if isPlan && !strings.HasPrefix(got, tt.start) {
				t.Errorf("returned text starts with %q, want it to start with %q", got[:len(tt.start)], tt.start)
			}
		})
	}
}
