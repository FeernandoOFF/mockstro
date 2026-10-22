// Package build prepares immutable installable artifacts independently of device handling.
package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/project"
)

// Artifact is a run-owned snapshot of an APK or iOS Simulator bundle. Source
// rebuilds never modify it; SHA256 covers the entire snapshot, including resources.
type Artifact struct {
	Path     string
	SHA256   string
	Platform string
	AppID    string
	System   project.BuildSystem
}

func (a Artifact) Exists() bool {
	st, err := os.Stat(a.Path)
	if err != nil {
		return false
	}
	switch a.Platform {
	case "ios":
		return st.IsDir() && strings.HasSuffix(a.Path, ".app")
	case "android":
		return st.Mode().IsRegular() && strings.HasSuffix(a.Path, ".apk")
	}
	return false
}

// Hash reads the snapshot, returning errors rather than treating unreadable files
// as an empty hash. Platform determines whether a bundle or a file is hashed.
func (a Artifact) Hash() (string, error) {
	if !a.Exists() {
		return "", fmt.Errorf("installable %s artifact does not exist: %s", a.Platform, a.Path)
	}
	if a.Platform == "ios" {
		return BundleSHA(a.Path)
	}
	return fsx.SHA256(a.Path)
}

func (a Artifact) Verify() error {
	sum, err := a.Hash()
	if err != nil {
		return err
	}
	if sum != a.SHA256 {
		return fmt.Errorf("artifact changed since build: %s", a.Path)
	}
	return nil
}

// Build runs exactly one configured build command in the source checkout and
// snapshots its single matching installable output. It never discovers devices or
// retries a failed native build through a different build system.
func Build(ctx context.Context, p *project.Project, ex execx.Runner, dir string, out io.Writer) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	if out == nil {
		out = io.Discard
	}
	b, err := p.Build.Resolve(p.Platform)
	if err != nil {
		return Artifact{}, err
	}
	root, err := filepath.Abs(p.SourceRoot())
	if err != nil {
		return Artifact{}, err
	}
	cmd := execx.Cmd{Dir: root, Stdout: out, Stderr: out}
	glob := b.Artifact
	switch b.System {
	case project.BuildCommand:
		cmd.Name, cmd.Args = b.Command[0], b.Command[1:]
	case project.BuildKotlin:
		target, task := "android", "buildAndroid"
		if p.Platform == "ios" {
			target, task = "iosSimulatorArm64", "buildIosAppIosSimulatorArm64"
		}
		cmd.Name, cmd.Args = "./kotlin", []string{"build", "-m", b.Module, "-p", target, "-v", b.Variant}
		variant := strings.ToUpper(b.Variant[:1]) + b.Variant[1:]
		if glob == "" {
			taskDir := filepath.Join("build", "tasks", "_"+b.Module+"_"+task+variant)
			if p.Platform == "ios" {
				glob = filepath.Join(taskDir, "bin", variant+"-iphonesimulator", "*.app")
			} else {
				glob = filepath.Join(taskDir, "*.apk")
			}
		}
	case project.BuildXcode:
		if b.Project == "" || b.Scheme == "" {
			return Artifact{}, fmt.Errorf("set platforms.ios.build.project and platforms.ios.build.scheme in .maestro/mockstro.yaml")
		}
		flag := "-project"
		if strings.HasSuffix(b.Project, ".xcworkspace") {
			flag = "-workspace"
		}
		projectPath := b.Project
		if !filepath.IsAbs(projectPath) {
			projectPath = filepath.Join(root, projectPath)
		}
		derived := filepath.Join(root, "build", "maestro-derived-data")
		cmd.Name, cmd.Args = "xcodebuild", []string{flag, projectPath, "-scheme", b.Scheme, "-configuration", b.Configuration, "-sdk", "iphonesimulator", "-destination", "generic/platform=iOS Simulator", "-derivedDataPath", derived, "CODE_SIGNING_ALLOWED=NO", "build"}
		if glob == "" {
			glob = filepath.Join(derived, "Build", "Products", b.Configuration+"-iphonesimulator", "*.app")
		}
	case project.BuildGradle:
		cmd.Name, cmd.Args = "./gradlew", []string{b.Task, "-q"}
	}
	fmt.Fprintf(out, "Building %s (incremental)...\n", cmd.String())
	if err := ex.Run(ctx, cmd); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return Artifact{}, fmt.Errorf("build failed: %s: %w", cmd.String(), err)
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	if !filepath.IsAbs(glob) {
		glob = filepath.Join(root, glob)
	}
	matches, err := filepath.Glob(glob)
	if err != nil {
		return Artifact{}, fmt.Errorf("artifact glob: %w", err)
	}
	var artifacts []string
	for _, path := range matches {
		if (Artifact{Path: path, Platform: p.Platform}).Exists() {
			artifacts = append(artifacts, path)
		}
	}
	if len(artifacts) != 1 {
		return Artifact{}, fmt.Errorf("expected one installable %s artifact at %s, found %d", p.Platform, glob, len(artifacts))
	}
	destDir, err := filepath.Abs(filepath.Join(dir, "artifacts"))
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return Artifact{}, err
	}
	// Each invocation owns a new snapshot, even when the same run directory is reused.
	snapshotDir, err := os.MkdirTemp(destDir, "build-")
	if err != nil {
		return Artifact{}, err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(snapshotDir)
		}
	}()
	artifact := Artifact{Platform: p.Platform, AppID: p.AppID, System: b.System}
	if p.Platform == "ios" {
		if err := validateBundle(ctx, artifacts[0], p.AppID, ex); err != nil {
			return Artifact{}, err
		}
		artifact.Path = filepath.Join(snapshotDir, filepath.Base(artifacts[0]))
		err = copyBundle(artifacts[0], artifact.Path)
	} else {
		artifact.Path = filepath.Join(snapshotDir, "app.apk")
		err = fsx.CopyAtomic(artifacts[0], artifact.Path)
	}
	if err != nil {
		return Artifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	artifact.SHA256, err = artifact.Hash()
	if err != nil {
		return Artifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "artifact.sha256"), []byte(artifact.SHA256+"  "+artifact.Path+"\n"), 0644); err != nil {
		return Artifact{}, err
	}
	complete = true
	return artifact, nil
}

func validateBundle(ctx context.Context, path, appID string, ex execx.Runner) error {
	data, err := os.ReadFile(filepath.Join(path, "Info.plist"))
	if err != nil {
		return err
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		xmlData, err := ex.Output(ctx, execx.Cmd{Name: "plutil", Args: []string{"-convert", "xml1", "-o", "-", filepath.Join(path, "Info.plist")}})
		if err != nil {
			return fmt.Errorf("read bundle Info.plist: %w", err)
		}
		data = []byte(xmlData)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	values := map[string]string{}
	var platforms []string
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read bundle Info.plist: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil {
			return err
		}
		for {
			tok, err = decoder.Token()
			if err != nil {
				return err
			}
			if value, ok := tok.(xml.StartElement); ok {
				switch value.Name.Local {
				case "string":
					var str string
					if err := decoder.DecodeElement(&str, &value); err != nil {
						return err
					}
					values[key] = str
				case "array":
					var array struct {
						Values []string `xml:"string"`
					}
					if err := decoder.DecodeElement(&array, &value); err != nil {
						return err
					}
					if key == "CFBundleSupportedPlatforms" {
						platforms = array.Values
					}
				default:
					if err := decoder.Skip(); err != nil {
						return err
					}
				}
				break
			}
		}
	}
	if appID != "" && values["CFBundleIdentifier"] != appID {
		return fmt.Errorf("bundle identifier %q does not match configured appId=%s", values["CFBundleIdentifier"], appID)
	}
	simulator := false
	for _, p := range platforms {
		if p == "iPhoneSimulator" {
			simulator = true
		}
	}
	if !simulator {
		return fmt.Errorf("%s is not an iOS Simulator bundle", path)
	}
	exe := values["CFBundleExecutable"]
	if exe == "" || filepath.Base(exe) != exe {
		return fmt.Errorf("invalid CFBundleExecutable in %s", path)
	}
	st, err := os.Stat(filepath.Join(path, exe))
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("bundle executable is not a file")
	}
	return nil
}

func copyBundle(src, dst string) error {
	// macOS commonly addresses the same temporary directory through /var and
	// /private/var. Compare resolved paths on both sides of the containment check.
	realSource, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".bundle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			// A snapshot must not keep a live dependency on the source checkout
			// or another external path through a preserved bundle symlink.
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve bundle symlink %s: %w", path, err)
			}
			rel, err := filepath.Rel(realSource, resolved)
			if err != nil || filepath.IsAbs(link) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("bundle symlink must be relative and remain inside the bundle: %s -> %s", path, link)
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported bundle file: %s", path)
		}
		if err := fsx.CopyAtomic(path, target); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func BundleSHA(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s\x00", link)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(h, f)
		f.Close()
		h.Write([]byte{0})
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
