package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type editorHarness struct {
	cmd  tea.Cmd
	done *editorDoneMsg
}

func (m *editorHarness) Init() tea.Cmd { return m.cmd }
func (m *editorHarness) View() string  { return "" }
func (m *editorHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if done, ok := msg.(editorDoneMsg); ok {
		m.done = &done
		return m, tea.Quit
	}
	return m, nil
}

func TestEditorReturnsControlToTUI(t *testing.T) {
	a := testApp(t)
	path := a.flows[0].Path
	for _, editor := range []string{"true", "false"} {
		t.Run(editor, func(t *testing.T) {
			t.Setenv("EDITOR", editor)
			m := &editorHarness{cmd: a.editFile(path)}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutRenderer()).Run()
			if err != nil || m.done == nil || m.done.path != path {
				t.Fatalf("editor did not return control: %v, %+v", err, m.done)
			}
			if (m.done.err != nil) != (editor == "false") {
				t.Fatalf("editor exit status lost: %v", m.done.err)
			}
		})
	}
}

func TestEditorCommandPreservesArgumentsAndLiteralSourcePath(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "My Editor")
	capture := filepath.Join(root, "arguments")
	write(t, script, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$EDITOR_CAPTURE\"\n")
	if err := os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	name := "flow 'quoted' $(touch UNEXPECTED).yaml"
	write(t, filepath.Join(root, name), "flow")
	t.Setenv("EDITOR", `"`+script+`" --wait "two words"`)
	t.Setenv("VISUAL", "must-not-be-used")
	t.Setenv("EDITOR_CAPTURE", capture)
	cmd, err := editorCommand(name, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(capture)
	want := "--wait\ntwo words\n" + filepath.Join(root, name) + "\n"
	if err != nil || string(got) != want {
		t.Fatalf("arguments %q, %v; want %q", got, err, want)
	}
	if _, err := os.Stat(filepath.Join(root, "UNEXPECTED")); !os.IsNotExist(err) {
		t.Fatal("source filename was interpreted as shell code")
	}
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "nano --view")
	cmd, err = editorCommand(name, root)
	if err != nil || !strings.Contains(cmd.Args[2], "exec nano --view") {
		t.Fatal(cmd, err)
	}
	t.Setenv("VISUAL", "")
	cmd, err = editorCommand(name, root)
	if err != nil || cmd.Args[2] != `exec vi "$@"` {
		t.Fatal(cmd, err)
	}
	if _, err := editorCommand("missing.yaml", root); err == nil {
		t.Fatal("opened a missing source")
	}
}

func TestEditBindingsAndRefreshAfterEditor(t *testing.T) {
	a := testApp(t)
	key(a, "2")
	key(a, "enter")
	if _, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}); cmd == nil {
		t.Fatal("flow edit did not launch an editor command")
	}
	// Every mock target retains its own editable source, including nested profiles.
	for _, target := range a.mock.all {
		if _, err := os.Stat(target.path); err != nil {
			t.Fatalf("%s source %q: %v", target.kind, target.path, err)
		}
	}
	key(a, "6")
	a.stubTab.show("b")
	a.stubTab.load(false)
	if cmd := a.stubTab.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}); cmd == nil {
		t.Fatal("stub edit did not launch an editor command")
	}
	path := a.store.StubPath("b")
	write(t, path, `{"name":"b","request":{"method":"GET","url":"/edited"},"response":{"status":201}}`)
	a.mock.stubsFor = "cached"
	_, reload := a.Update(editorDoneMsg{path: path})
	if reload == nil || a.mock.stubsFor != "" {
		t.Fatal("editor completion did not invalidate fixtures")
	}
	a.Update(reload())
	a.stubTab.load(false)
	if !strings.Contains(strings.Join(a.stubTab.stubLines, "\n"), "/edited") {
		t.Fatal("stub preview did not reload the edited source")
	}
	// A save with invalid JSON must remain editable rather than crashing the preview.
	write(t, path, "{ invalid")
	_, reload = a.Update(editorDoneMsg{path: path, err: errors.New("editor failed")})
	a.Update(reload())
	a.stubTab.load(false)
	if !strings.Contains(a.activity[len(a.activity)-2].text, "editor failed") {
		t.Fatal("editor failure was not reported")
	}
}
