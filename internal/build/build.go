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

func Build(ctx context.Context, p *project.Project, platform string, ex execx.Runner, dir string, out io.Writer) (string, string, error) {
	if out == nil {
		out = io.Discard
	}
	root := p.SourceRoot()
	cmd := execx.Cmd{Dir: root, Stdout: out, Stderr: out}
	glob := p.APKGlob
	if p.BuildSystem == "kotlin-toolchain" {
		module, target := p.KotlinAndroidModule, "android"
		task := "buildAndroid"
		if platform == "ios" {
			module, target, task = p.KotlinIOSModule, "iosSimulatorArm64", "buildIosAppIosSimulatorArm64"
		}
		if module == "" || p.KotlinVariant != "debug" && p.KotlinVariant != "release" {
			return "", "", fmt.Errorf("configure build.module and build.variant (debug or release) for %s", platform)
		}
		cmd.Name, cmd.Args = "./kotlin", []string{"build", "-m", module, "-p", target, "-v", p.KotlinVariant}
		variant := strings.ToUpper(p.KotlinVariant[:1]) + p.KotlinVariant[1:]
		if platform == "ios" {
			glob = p.IOSAppGlob
		}
		if glob == "" {
			taskDir := filepath.Join("build", "tasks", "_"+module+"_"+task+variant)
			if platform == "ios" {
				glob = filepath.Join(taskDir, "bin", variant+"-iphonesimulator", "*.app")
			} else {
				glob = filepath.Join(taskDir, "*.apk")
			}
		}
	} else if platform == "ios" {
		if p.IOSProject == "" || p.IOSScheme == "" {
			return "", "", fmt.Errorf("set platforms.ios.build.project and platforms.ios.build.scheme in .maestro/mockstro.yaml")
		}
		flag := "-project"
		if strings.HasSuffix(p.IOSProject, ".xcworkspace") {
			flag = "-workspace"
		}
		derived := filepath.Join(root, "build", "maestro-derived-data")
		cmd.Name, cmd.Args = "xcodebuild", []string{flag, filepath.Join(root, p.IOSProject), "-scheme", p.IOSScheme, "-configuration", p.IOSConfig, "-sdk", "iphonesimulator", "-destination", "generic/platform=iOS Simulator", "-derivedDataPath", derived, "CODE_SIGNING_ALLOWED=NO", "build"}
		glob = p.IOSAppGlob
		if glob == "" {
			glob = filepath.Join(derived, "Build", "Products", p.IOSConfig+"-iphonesimulator", "*.app")
		}
	} else {
		cmd.Name, cmd.Args = "./gradlew", []string{p.GradleTask, "-q"}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	fmt.Fprintf(out, "Building %s (incremental)...\n", cmd.String())
	if err := ex.Run(ctx, cmd); err != nil {
		return "", "", fmt.Errorf("build failed: %s: %w", cmd.String(), err)
	}
	if !filepath.IsAbs(glob) {
		glob = filepath.Join(root, glob)
	}
	matches, err := filepath.Glob(glob)
	if err != nil {
		return "", "", fmt.Errorf("artifact glob: %w", err)
	}
	var artifacts []string
	for _, path := range matches {
		st, err := os.Stat(path)
		if err == nil && (platform == "ios" && st.IsDir() || platform != "ios" && st.Mode().IsRegular()) {
			artifacts = append(artifacts, path)
		}
	}
	if len(artifacts) != 1 {
		return "", "", fmt.Errorf("expected one installable artifact at %s, found %d", glob, len(artifacts))
	}
	destDir := filepath.Join(dir, "artifacts")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", "", err
	}
	var dst, sum string
	if platform == "ios" {
		if err := validateBundle(ctx, artifacts[0], p.AppID, ex); err != nil {
			return "", "", err
		}
		dst = filepath.Join(destDir, filepath.Base(artifacts[0]))
		if err := copyBundle(artifacts[0], dst); err != nil {
			return "", "", err
		}
		sum, err = BundleSHA(dst)
	} else {
		dst = filepath.Join(destDir, "app.apk")
		err = fsx.CopyAtomic(artifacts[0], dst)
		if err == nil {
			sum, err = fsx.SHA256(dst)
		}
	}
	if err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(dir, "apk.sha256"), []byte(sum+"  "+dst+"\n"), 0644); err != nil {
		return "", "", err
	}
	return dst, sum, nil
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
