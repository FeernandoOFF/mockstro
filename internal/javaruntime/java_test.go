package javaruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

func TestFindJava(t *testing.T) {
	for _, output := range []string{"missing", `java version "1.8.0_432"`, `openjdk version "16.0.2"`, "unknown version", `openjdk 21.0.5`, `java version "17.0.13"`} {
		t.Run(output, func(t *testing.T) {
			t.Setenv("JAVA_HOME", "")
			ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
				if output == "missing" {
					return "", errors.New("macOS launcher has no runtime")
				}
				return output, nil
			}}
			_, err := Find(context.Background(), ex)
			wantOK := strings.Contains(output, "21.0") || strings.Contains(output, "17.0")
			if (err == nil) != wantOK {
				t.Fatalf("Find = %v, want success %v", err, wantOK)
			}
		})
	}
}

func TestFindOffPATHAndActivate(t *testing.T) {
	for _, source := range []string{"JAVA_HOME", "mac-user-jdk", "sdkman"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("PATH", "/usr/bin:/bin")
			t.Setenv("JAVA_HOME", "")
			home := filepath.Join(t.TempDir(), "jdk")
			if source == "JAVA_HOME" {
				home = filepath.Join(t.TempDir(), "jdk with spaces")
				t.Setenv("JAVA_HOME", home)
			}
			if source == "mac-user-jdk" {
				home = filepath.Join(os.Getenv("HOME"), "Library/Java/JavaVirtualMachines/test.jdk/Contents/Home")
			} else if source == "sdkman" {
				home = filepath.Join(os.Getenv("HOME"), ".sdkman/candidates/java/current")
			}
			if err := os.MkdirAll(home, 0755); err != nil {
				t.Fatal(err)
			}
			command := filepath.Join(home, "bin", "java")
			ex := &execx.Fake{Paths: map[string]string{command: command}, Handler: func(c execx.Cmd) (string, error) {
				if c.Name == command {
					return `openjdk version "17.0.13"`, nil
				}
				return `java version "1.8.0_432"`, nil
			}}
			got, err := Find(context.Background(), ex)
			if err != nil || got != home {
				t.Fatalf("Find = %q, %v", got, err)
			}
			if err := Activate(got); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("JAVA_HOME") != home || !strings.HasPrefix(os.Getenv("PATH"), filepath.Join(home, "bin")+":") {
				t.Fatal("JDK not activated for child processes")
			}
		})
	}
}

func TestEnsureWithHomebrew(t *testing.T) {
	for _, scenario := range []string{"install", "no-brew", "install-fails", "bad-runtime"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("JAVA_HOME", "")
			t.Setenv("PATH", os.Getenv("PATH"))
			installed := false
			prefix := filepath.Join(t.TempDir(), "openjdk@17")
			ex := &execx.Fake{Paths: map[string]string{}, Handler: func(c execx.Cmd) (string, error) {
				switch c.String() {
				case "java -version":
					return "", errors.New("no Java")
				case "/opt/homebrew/bin/brew install openjdk@17":
					if scenario == "install-fails" {
						return "", errors.New("brew failed")
					}
					installed = true
					return "", nil
				case "/opt/homebrew/bin/brew --prefix openjdk@17":
					return prefix, nil
				case filepath.Join(prefix, "bin", "java") + " -version":
					if installed && scenario != "bad-runtime" {
						return `openjdk version "17.0.13"`, nil
					}
				}
				return "", errors.New("unexpected command: " + c.String())
			}}
			if scenario != "no-brew" {
				ex.Paths["/opt/homebrew/bin/brew"] = "/opt/homebrew/bin/brew"
			}
			err := Ensure(context.Background(), ex, func(string, ...any) {})
			if scenario == "install" {
				if err != nil || os.Getenv("JAVA_HOME") != prefix {
					t.Fatalf("install: %v", err)
				}
			} else if err == nil {
				t.Fatal("expected setup to fail")
			}
			if scenario == "no-brew" && len(ex.Calls) != 1 {
				t.Fatalf("attempted install without brew: %v", ex.Calls)
			}
		})
	}
}
