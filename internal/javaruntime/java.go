// Package javaruntime finds Java 17+ and installs it through Homebrew when needed.
package javaruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
)

const Hint = "install Java 17+ or install Homebrew and rerun setup"

var version = regexp.MustCompile(`(?:openjdk|java)\s+(?:version\s+)?"?(\d+)(?:\.(\d+))?`)

func working(ctx context.Context, ex execx.Runner, command string) bool {
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if ex.Run(ctx, execx.Cmd{Name: command, Args: []string{"-version"}, Stdout: &out, Stderr: &out}) != nil {
		return false
	}
	m := version.FindStringSubmatch(out.String())
	if len(m) == 0 {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	return major >= 17
}

// Find returns an empty home for a working Java on PATH, or a discovered JDK home.
func Find(ctx context.Context, ex execx.Runner) (string, error) {
	if working(ctx, ex, "java") {
		// Maestro also consults JAVA_HOME, which may disagree with PATH.
		if home := os.Getenv("JAVA_HOME"); home != "" && working(ctx, ex, filepath.Join(home, "bin", "java")) {
			return home, nil
		}
		if path, err := ex.LookPath("java"); err == nil {
			if real, err := filepath.EvalSymlinks(path); err == nil && filepath.Base(filepath.Dir(real)) == "bin" && real != "/usr/bin/java" {
				return filepath.Dir(filepath.Dir(real)), nil
			}
		}
		return "", nil
	}
	candidates := []string{os.Getenv("JAVA_HOME")}
	home, _ := os.UserHomeDir()
	patterns := []string{
		"/Library/Java/JavaVirtualMachines/*/Contents/Home",
		filepath.Join(home, "Library/Java/JavaVirtualMachines/*/Contents/Home"),
		"/opt/homebrew/opt/openjdk*/libexec/openjdk.jdk/Contents/Home",
		"/usr/local/opt/openjdk*/libexec/openjdk.jdk/Contents/Home",
		"/opt/homebrew/opt/openjdk*", "/usr/local/opt/openjdk*",
		"/home/linuxbrew/.linuxbrew/opt/openjdk*",
		"/home/linuxbrew/.linuxbrew/opt/openjdk*/libexec",
		"/usr/lib/jvm/*", "/usr/java/*",
		"/Applications/Android Studio.app/Contents/jbr/Contents/Home",
		"/opt/android-studio/jbr", filepath.Join(home, ".sdkman/candidates/java/current"),
		filepath.Join(home, ".jenv/versions/*"),
	}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		candidates = append(candidates, matches...)
	}
	for _, home := range candidates {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if home == "" {
			continue
		}
		command := filepath.Join(home, "bin", "java")
		if _, err := ex.LookPath(command); err == nil && working(ctx, ex, command) {
			return home, nil
		}
	}
	return "", fmt.Errorf("Java 17+ was not found; %s", Hint)
}

// Activate makes the selected JDK available to every child, including Maestro and WireMock.
// Call before starting concurrent work. Shell startup files are left alone.
func Activate(home string) error {
	if home == "" {
		return os.Unsetenv("JAVA_HOME") // ignore a stale JAVA_HOME when PATH Java works
	}
	if err := os.Setenv("JAVA_HOME", home); err != nil {
		return err
	}
	bin := filepath.Join(home, "bin")
	if !strings.HasPrefix(os.Getenv("PATH"), bin+string(os.PathListSeparator)) {
		return os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return nil
}

// Ensure installs automatically if discovery fails, then verifies the installed runtime.
func Ensure(ctx context.Context, ex execx.Runner, logf func(string, ...any)) error {
	home, err := Find(ctx, ex)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		brew, err := ex.LookPath("brew")
		if err != nil {
			for _, path := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew", "/home/linuxbrew/.linuxbrew/bin/brew"} {
				if brew, err = ex.LookPath(path); err == nil {
					break
				}
			}
		}
		if err != nil {
			return fmt.Errorf("Java 17+ was not found and Homebrew is unavailable; %s", Hint)
		}
		logf("Installing Java 17 with Homebrew...")
		if err := ex.Run(ctx, execx.Cmd{Name: brew, Args: []string{"install", "openjdk@17"}, Stdout: os.Stdout, Stderr: os.Stderr}); err != nil {
			return fmt.Errorf("brew install openjdk@17: %w", err)
		}
		prefix, err := ex.Output(ctx, execx.Cmd{Name: brew, Args: []string{"--prefix", "openjdk@17"}})
		if err != nil {
			return fmt.Errorf("find Homebrew Java: %w", err)
		}
		home = strings.TrimSpace(prefix)
		if !working(ctx, ex, filepath.Join(home, "bin", "java")) {
			return fmt.Errorf("Homebrew Java 17 could not start at %s", home)
		}

	}
	if err := Activate(home); err != nil {
		return err
	}
	logf("Java 17+ is ready.")
	return nil
}
