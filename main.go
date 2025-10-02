package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
)

// Configuration flags
var (
	verbose    bool
	showHelp   bool
	showVersion bool
	outputFile string
	quiet      bool
	noColor    bool
	forceColor bool
	fsType     string
	bufferSize int // In MB
	preallocate bool
	dualOutput bool
)

// Version information
const (
	Version = "1.0.0"
	BuildDate = "2025-09-29"
)

// ANSI color codes
const (
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorReset  = "\033[0m"
)

// OCI JSON structures
type OCIIndex struct {
	Manifests []OCIManifest `json:"manifests"`
}

type OCIManifest struct {
	Config OCDescriptor `json:"config"`
}

type OCDescriptor struct {
	Digest string `json:"digest"`
}

// ConversionContext holds all state and configuration for a conversion task.
type ConversionContext struct {
	TempDir       string
	OciLayoutPath string // Directory for the raw OCI image
	UnpackedPath  string // Directory for the final, unpacked rootfs
	ImagePath     string
	SquashfsPath  string
	MountPoint    string
	LoopDevicePath string // Explicit loop device path like "/dev/loop0"
	FinalPath     string
	FinalSquashfsPath string
	ImageRef      string
	FsType        string
	BufferSize    int // In MB
	Preallocate   bool
	DualOutput    bool
	Verbose       bool
	Quiet         bool
	NoColor       bool
}

func init() {
	flag.BoolVar(&showHelp, "h", false, "Show this help message")
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	flag.BoolVar(&verbose, "v", false, "Enable verbose output with progress details")
	flag.StringVar(&outputFile, "o", "", "Output file path (default: <image-name>.img)")
	flag.BoolVar(&quiet, "q", false, "Quiet mode (minimal output, just final path)")
	flag.BoolVar(&noColor, "no-color", false, "Disable colored output")
	flag.StringVar(&fsType, "fs", "ext4", "Filesystem type for the image (e.g., ext4, xfs)")
	flag.IntVar(&bufferSize, "s", 50, "Buffer size in MB to add to the image")
	flag.BoolVar(&preallocate, "preallocate", false, "Preallocate disk space (fallocate) instead of sparse allocation")
	flag.BoolVar(&dualOutput, "dual-output", false, "Also generate a squashfs image alongside the primary filesystem")
}

func main() {
	flag.Parse()

	if showVersion {
		fmt.Printf("fsify version %s (built %s)\n", Version, BuildDate)
		return
	}

	if showHelp {
		showUsage()
		return
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, colorize("Error: This program requires root privileges for mount operations.", "red", false))
		fmt.Fprintln(os.Stderr, "Please run with sudo.")
		os.Exit(1)
	}

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, colorize("Error: Missing Docker image reference", "red", false))
		showUsage()
		os.Exit(1)
	}

	imageRef := args[0]

	isTerm := isTerminal()
	if verbose {
		fmt.Printf("Debug: isTerminal()=%v, forceColor=%v, noColor=%v\n", isTerm, forceColor, noColor)
	}
	if !isTerm && !forceColor {
		quiet = true
		noColor = true
	}

	if err := checkPrerequisites(fsType, dualOutput); err != nil {
		fmt.Fprintf(os.Stderr, "%s Error: Missing prerequisites - %v\n", colorize("❌", "red", noColor), err)
		suggestPrerequisiteInstallation()
		os.Exit(1)
	}

	outputFormat := fsType
	if dualOutput {
		outputFormat = fsType + "+squashfs"
	}

	if !quiet {
		fmt.Printf("%s Converting Docker image '%s' to %s filesystem...\n", colorize("🚀", "blue", noColor), imageRef, outputFormat)
	}

	outputPath, err := createFsFromImage(imageRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", colorize("❌ Fatal Error:", "red", noColor), err)
		os.Exit(1)
	}

	if quiet {
		fmt.Println(outputPath)
	} else {
		fmt.Printf("\n%s Successfully created image: %s\n", colorize("✅", "green", noColor), outputPath)
	}
}

func showUsage() {
	fmt.Println(`fsify - Convert Docker images to bootable filesystem images

USAGE:
    sudo fsify [OPTIONS] <docker-image>

EXAMPLES:
    sudo fsify nginx:latest                    # Basic usage (idiot path)
    sudo fsify -v -fs xfs -s 100 alpine:3.18   # XFS filesystem
    sudo fsify -o my-image.img ubuntu:22.04   # Custom output
    sudo fsify --preallocate -v nginx:latest  # Preallocated disk
    sudo fsify --dual-output redis:7.0        # Both ext4 + squashfs

OPTIONS:
    -h, --help            Show this help message
    --version             Show version information
    -v, --verbose         Enable verbose output with progress details
    -o, --output FILE     Output file path (default: <image-name>.img)
    -q, --quiet           Quiet mode (minimal output, just final path)
    --no-color            Disable colored output
    -fs, --filesystem     Filesystem type (ext4, xfs, btrfs) (default: ext4)
    -s, --size-buffer     Extra space in MB to add to the image (default: 50)
    --preallocate         Preallocate disk space instead of sparse allocation
    --dual-output         Generate both primary filesystem AND squashfs image

REQUIREMENTS:
    - Root privileges (for mount/mkfs operations)
    - skopeo (for pulling OCI images)
    - umoci (for unpacking OCI images into a rootfs)
    - Coreutils (dd, du, cp, fallocate)
    - Filesystem utilities (mkfs.<type>, mount, umount)
    - Optional: pv (for progress monitoring during copy)
    - Optional: mksquashfs (for --dual-output mode)

FEATURES:
    - Cross-filesystem support with automatic flag detection
    - OCI config embedding for Docker semantics compatibility
    - Automatic loop device cleanup
    - Native Go progress bar with real-time file copying progress
    - Dual output mode for both bootable and compressed images
    - Robust error handling with helpful hints`)
}

func colorize(text, color string, noColorFlag bool) string {
	if noColorFlag || noColor || !isTerminal() {
		// Strip any existing color codes from text when colors are disabled
		return stripColorCodes(text)
	}

	// Map color names to ANSI color codes
	var colorCode string
	switch strings.ToLower(color) {
	case "red":
		colorCode = ColorRed
	case "green":
		colorCode = ColorGreen
	case "yellow":
		colorCode = ColorYellow
	case "blue":
		colorCode = ColorBlue
	case "cyan":
		colorCode = ColorCyan
	default:
		// If color name is not recognized, don't apply any color
		return text
	}

	return colorCode + text + ColorReset
}

func stripColorCodes(text string) string {
	// Remove ANSI color codes
	result := strings.ReplaceAll(text, ColorRed, "")
	result = strings.ReplaceAll(result, ColorGreen, "")
	result = strings.ReplaceAll(result, ColorYellow, "")
	result = strings.ReplaceAll(result, ColorBlue, "")
	result = strings.ReplaceAll(result, ColorCyan, "")
	result = strings.ReplaceAll(result, ColorReset, "")
	return result
}

func isTerminal() bool {
	// Check all standard file descriptors for terminal detection
	stdoutIsTerminal := isFileTerminal(os.Stdout)
	stderrIsTerminal := isFileTerminal(os.Stderr)
	stdinIsTerminal := isFileTerminal(os.Stdin)
	return stdoutIsTerminal || stderrIsTerminal || stdinIsTerminal
}

func isFileTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func (ctx *ConversionContext) runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)

	if ctx.Verbose {
		fmt.Printf("%s Running: %s %s\n", colorize("│", "blue", ctx.NoColor), name, strings.Join(args, " "))
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start command '%s': %w", name, err)
		}
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				fmt.Printf("%s %s\n", colorize("│", "cyan", ctx.NoColor), scanner.Text())
			}
		}()
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				fmt.Printf("%s %s\n", colorize("│", "yellow", ctx.NoColor), scanner.Text())
			}
		}()
		return cmd.Wait()
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command '%s %s' failed: %v\n--- Output ---\n%s", name, strings.Join(args, " "), err, string(output))
	}
	return nil
}

func (ctx *ConversionContext) runStep(message string, task func() error) error {
	if !ctx.Quiet && isTerminal() {
		stopSpinner := make(chan struct{})
		go func() {
			icons := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			i := 0
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopSpinner:
					fmt.Printf("\r")
					return
				case <-ticker.C:
					fmt.Printf("\r%s %s", colorize(icons[i], "cyan", ctx.NoColor), message)
					i = (i + 1) % len(icons)
				}
			}
		}()
		err := task()
		close(stopSpinner)
		time.Sleep(120 * time.Millisecond)
		return err
	}
	return task()
}

func createFsFromImage(imageRef string) (string, error) {
	ctx := &ConversionContext{
		ImageRef:    imageRef,
		Verbose:     verbose,
		Quiet:       quiet,
		NoColor:     noColor,
		FsType:      fsType,
		BufferSize:  bufferSize,
		Preallocate: preallocate,
		DualOutput:  dualOutput,
	}

	tempDir, err := os.MkdirTemp("", "fsify-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	ctx.TempDir = tempDir
	defer os.RemoveAll(tempDir)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Printf("\n%s Interrupt received, cleaning up...\n", colorize("⚠️", "yellow", ctx.NoColor))
		_ = unmountImage(ctx)
		os.RemoveAll(tempDir)
		os.Exit(1)
	}()

	ctx.OciLayoutPath = filepath.Join(tempDir, "oci-layout")
	ctx.UnpackedPath = filepath.Join(tempDir, "unpacked-rootfs")
	ctx.ImagePath = filepath.Join(tempDir, "fs-image.img")
	ctx.SquashfsPath = filepath.Join(tempDir, "fs-image.squashfs")
	ctx.MountPoint = filepath.Join(tempDir, "mnt")

	if outputFile == "" {
		// Simplified output filename: nginx-latest.img
		parts := strings.Split(imageRef, "/")
		imagePart := parts[len(parts)-1]
		ctx.FinalPath = imagePart + ".img"
		if dualOutput {
			ctx.FinalSquashfsPath = imagePart + ".squashfs"
		}
	} else {
		ctx.FinalPath = outputFile
		if dualOutput {
			base := strings.TrimSuffix(outputFile, filepath.Ext(outputFile))
			ctx.FinalSquashfsPath = base + ".squashfs"
		}
	}

	dirs := []string{ctx.OciLayoutPath, ctx.UnpackedPath, ctx.MountPoint}
	if dualOutput {
		dirs = append(dirs, filepath.Dir(ctx.SquashfsPath))
	}
	for _, dir := range dirs {
		if err := os.Mkdir(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create dir %s: %w", dir, err)
		}
	}
	defer unmountImage(ctx)

	steps := []struct {
		message string
		icon    string
		task    func() error
	}{
		{"Downloading OCI image", "📥", func() error { return downloadOciImage(ctx) }},
		{"Unpacking image layers", "📦", func() error { return unpackOciImage(ctx) }},
		{"Extracting OCI config", "📝", func() error { return extractOciConfig(ctx) }},
		{"Calculating disk size", "📏", func() error { return createImageFile(ctx) }},
		{"Creating filesystem", "💾", func() error { return createFilesystem(ctx) }},
		{"Mounting image", "🔌", func() error { return mountImage(ctx) }},
		{"Copying files to image", "📋", func() error { return copyRootfsToImage(ctx) }},
		{"Unmounting image", "🔌", func() error { return unmountImage(ctx) }},
	}

	if dualOutput {
		steps = append(steps, struct {
			message string
			icon    string
			task    func() error
		}{"Creating squashfs image", "🗜️", func() error { return createSquashfsImage(ctx) }})
	}

	for _, step := range steps {
		// Special handling for copy step - it has its own progress bar
		if step.message == "Copying files to image" {
			if !ctx.Quiet {
				icon := "Copying files"
				if !ctx.NoColor && isTerminal() {
					icon = "📋"
				}
				fmt.Printf("%s files to image...\n", icon)
			}
			if err := step.task(); err != nil {
				if !ctx.Quiet {
					fmt.Printf("\r%s %s ... Failed\n", "❌", step.message)
				}
				return "", fmt.Errorf("step '%s' failed: %w", step.message, err)
			}
			if !ctx.Quiet {
				fmt.Printf("\r%s %s ... Done\n", "✅", step.message)
			}
		} else {
			// Other steps use spinner
			if err := ctx.runStep(step.message+"...", step.task); err != nil {
				if !ctx.Quiet {
					fmt.Printf("\r%s %s ... Failed\n", "❌", step.message)
				}
				return "", fmt.Errorf("step '%s' failed: %w", step.message, err)
			}
			if !ctx.Quiet {
				fmt.Printf("\r%s %s ... Done\n", step.icon, step.message)
			}
		}
	}

	if !ctx.Quiet {
		fmt.Printf("%s Moving final image...", colorize("🚚", "yellow", ctx.NoColor))
	}
	if err := os.Rename(ctx.ImagePath, ctx.FinalPath); err != nil {
		return "", fmt.Errorf("failed to move final image to %s: %w", ctx.FinalPath, err)
	}
	if !ctx.Quiet {
		fmt.Printf("\r%s Moved final image to %s\n", colorize("🚚", "green", ctx.NoColor), ctx.FinalPath)
	}

	if dualOutput {
		if err := os.Rename(ctx.SquashfsPath, ctx.FinalSquashfsPath); err != nil {
			return "", fmt.Errorf("failed to move squashfs image to %s: %w", ctx.FinalSquashfsPath, err)
		}
		if !ctx.Quiet {
			fmt.Printf("%s Created squashfs image: %s\n", colorize("🗜️", "green", ctx.NoColor), ctx.FinalSquashfsPath)
		}
	}

	// Always return the primary (bootable) image path
	return filepath.Abs(ctx.FinalPath)
}

func checkPrerequisites(fs string, checkSquashfs bool) error {
	tools := []string{"skopeo", "umoci", "mount", "umount", "dd", "du", "cp", "mkfs." + fs}
	if checkSquashfs {
		tools = append(tools, "mksquashfs")
	}
	var missing []string
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required tools: %s", strings.Join(missing, ", "))
	}
	return nil
}

func suggestPrerequisiteInstallation() {
	fmt.Println("\nPlease install the required tools. For example:")
	fmt.Printf("  %s sudo apt-get update && sudo apt-get install skopeo umoci coreutils util-linux e2fsprogs\n", colorize("Debian/Ubuntu:", "cyan", noColor))
	fmt.Printf("  %s sudo dnf install skopeo umoci coreutils util-linux e2fsprogs\n", colorize("Fedora/CentOS/RHEL:", "cyan", noColor))
	fmt.Printf("  %s brew install skopeo umoci coreutils\n", colorize("macOS (with MacPorts):", "cyan", noColor))
	fmt.Println("\nFor additional filesystems:")
	fmt.Printf("  %s sudo apt-get install xfsprogs btrfs-progs\n", colorize("For XFS/Btrfs:", "cyan", noColor))
}

func downloadOciImage(ctx *ConversionContext) error {
	// Try local Docker daemon first
	localErr := ctx.runCommand("skopeo", "copy", fmt.Sprintf("docker-daemon:%s", ctx.ImageRef), fmt.Sprintf("oci:%s:latest", ctx.OciLayoutPath))
	if localErr == nil {
		if ctx.Verbose {
			fmt.Printf("%s Successfully copied from local Docker daemon\n", colorize("│", "cyan", ctx.NoColor))
		}
		return nil
	}

	// If local copy fails, try remote registry
	if ctx.Verbose {
		fmt.Printf("%s Local Docker daemon copy failed, trying remote registry...\n", colorize("│", "yellow", ctx.NoColor))
	}
	return ctx.runCommand("skopeo", "copy", fmt.Sprintf("docker://%s", ctx.ImageRef), fmt.Sprintf("oci:%s:latest", ctx.OciLayoutPath))
}

func unpackOciImage(ctx *ConversionContext) error {
	return ctx.runCommand("umoci", "unpack", "--image", fmt.Sprintf("%s:latest", ctx.OciLayoutPath), ctx.UnpackedPath)
}

func createImageFile(ctx *ConversionContext) error {
	cmd := exec.Command("du", "-sk", ctx.UnpackedPath)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get directory size: %w", err)
	}
	parts := strings.Fields(string(output))
	if len(parts) < 1 {
		return fmt.Errorf("failed to parse du output: %q", string(output))
	}
	sizeKB, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Errorf("failed to parse size %q: %w", parts[0], err)
	}

	// Auto-adjust buffer size for large images (>1GB) if using default buffer
	bufferKB := ctx.BufferSize * 1024
	const defaultBufferMB = 50
	const largeImageThresholdKB = 1048576 // 1GB in KB
	const largeImageBufferMB = 100        // 100MB buffer for large images
	
	if ctx.BufferSize == defaultBufferMB && sizeKB > largeImageThresholdKB {
		bufferKB = largeImageBufferMB * 1024
		if ctx.Verbose {
			fmt.Printf("%s Image size >1GB detected, auto-increasing buffer to %dMB\n", colorize("│", "yellow", ctx.NoColor), largeImageBufferMB)
		}
	}
	totalSizeKB := sizeKB + bufferKB
	totalSizeBytes := totalSizeKB * 1024

	if ctx.Verbose {
		fmt.Printf("%s Rootfs: %d KB, Buffer: %d KB, Total: %d KB\n", colorize("│", "blue", ctx.NoColor), sizeKB, bufferKB, totalSizeKB)
	}

	if ctx.Preallocate {
		// Use fallocate for preallocated space
		return ctx.runCommand("fallocate", "-l", strconv.Itoa(totalSizeBytes), ctx.ImagePath)
	} else {
		// Use sparse allocation with dd
		return ctx.runCommand("dd", "if=/dev/zero", "of="+ctx.ImagePath, "bs=1K", "count=0", "seek="+strconv.Itoa(totalSizeKB))
	}
}

func createFilesystem(ctx *ConversionContext) error {
	mkfsCmd := "mkfs." + ctx.FsType
	args := []string{ctx.ImagePath}

	// Cross-filesystem mkfs flags
	mkfsFlags := map[string][]string{
		"ext4":  {"-F"},
		"xfs":   {"-f"},
		"btrfs": {"-f"},
	}

	if flags, exists := mkfsFlags[ctx.FsType]; exists {
		args = append(flags, args...)
	}

	err := ctx.runCommand(mkfsCmd, args...)
	if err != nil {
		// Provide helpful hints for common filesystem errors
		switch ctx.FsType {
		case "ext4":
			fmt.Fprintf(os.Stderr, "\n%s Hint: Make sure e2fsprogs is installed\n", colorize("💡", "yellow", ctx.NoColor))
		case "xfs":
			fmt.Fprintf(os.Stderr, "\n%s Hint: Make sure xfsprogs is installed\n", colorize("💡", "yellow", ctx.NoColor))
		case "btrfs":
			fmt.Fprintf(os.Stderr, "\n%s Hint: Make sure btrfs-progs is installed\n", colorize("💡", "yellow", ctx.NoColor))
		}
	}
	return err
}

func mountImage(ctx *ConversionContext) error {
	// Find a free loop device and attach our image file to it
	cmd := exec.Command("losetup", "--find", "--show", ctx.ImagePath)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to find loop device for %s: %w, output: %s", ctx.ImagePath, err, string(out))
	}
	ctx.LoopDevicePath = strings.TrimSpace(string(out))
	if ctx.LoopDevicePath == "" {
		return fmt.Errorf("losetup did not return a device path")
	}

	if ctx.Verbose {
		fmt.Printf("%s Attached image to loop device %s\n", colorize("│", "cyan", ctx.NoColor), ctx.LoopDevicePath)
	}

	// Now mount the specific loop device
	return ctx.runCommand("mount", ctx.LoopDevicePath, ctx.MountPoint)
}

func copyRootfsToImage(ctx *ConversionContext) error {
	// --- Step 1: Calculate total size for the progress bar ---
	var totalSize int64
	err := filepath.WalkDir(ctx.UnpackedPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			totalSize += info.Size()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to calculate total size of rootfs: %w", err)
	}

	// --- Step 2: Set up the progress bar ---
	// Use plain text when colors are disabled
	description := "Copying files to image"
	if !ctx.NoColor && isTerminal() {
		description = "📋 Copying files to image"
	}

	bar := progressbar.NewOptions64(totalSize,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(15),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",  // Plain text
			SaucerHead:    ">",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	// --- Step 3: Walk and copy, updating the bar ---
	// Walk the actual rootfs subdirectory, not the unpacked parent
	actualRootfs := filepath.Join(ctx.UnpackedPath, "rootfs")
	return filepath.WalkDir(actualRootfs, func(srcPath string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get the relative path to reconstruct the destination
		relPath, err := filepath.Rel(actualRootfs, srcPath)
		if err != nil {
			return err
		}
		destPath := filepath.Join(ctx.MountPoint, relPath)

		// Get file info to double-check type
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("failed to get info for %s: %w", srcPath, err)
		}

		if info.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		// It's a file, so copy it
		if info.Mode()&os.ModeSymlink != 0 {
			// Handle symlinks
			target, err := os.Readlink(srcPath)
			if err != nil {
				return fmt.Errorf("failed to read symlink %s: %w", srcPath, err)
			}
			return os.Symlink(target, destPath)
		}

		// Regular file
		srcFile, err := os.Open(srcPath)
		if err != nil {
			return fmt.Errorf("failed to open source file %s: %w", srcPath, err)
		}
		defer srcFile.Close()

		// Preserve file permissions
		destFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
		if err != nil {
			return fmt.Errorf("failed to create destination file %s: %w", destPath, err)
		}
		defer destFile.Close()

		// Create a multi-writer that writes to the destination file AND the progress bar
		writer := io.MultiWriter(destFile, bar)

		_, err = io.Copy(writer, srcFile)
		return err
	})
}

func extractOciConfig(ctx *ConversionContext) error {
	// Find the config.json file in the OCI layout
	configPath := filepath.Join(ctx.OciLayoutPath, "blobs", "sha256")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil // Skip if no config available
	}

	// Read the index.json to find the config file
	indexPath := filepath.Join(ctx.OciLayoutPath, "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return nil // Skip if can't read index
	}

	// Parse the JSON properly
	var index OCIIndex
	if err := json.Unmarshal(indexData, &index); err != nil {
		return nil // Skip if can't parse JSON
	}

	if len(index.Manifests) == 0 {
		return nil // No manifests found
	}

	configDigest := index.Manifests[0].Config.Digest
	if configDigest == "" {
		return nil // No config digest found
	}

	// Copy the config file to the rootfs
	if strings.HasPrefix(configDigest, "sha256:") {
		configDigest = strings.TrimPrefix(configDigest, "sha256:")
		sourceConfig := filepath.Join(configPath, configDigest)
		if _, err := os.Stat(sourceConfig); err == nil {
			// Create /etc/fsify-entrypoint in the rootfs
			entrypointDir := filepath.Join(ctx.UnpackedPath, "etc")
			if err := os.MkdirAll(entrypointDir, 0755); err != nil {
				return fmt.Errorf("failed to create /etc directory: %w", err)
			}

			// Copy the config file as entrypoint info
			entrypointFile := filepath.Join(entrypointDir, "fsify-entrypoint")
			return ctx.runCommand("cp", sourceConfig, entrypointFile)
		}
	}

	return nil
}

func createSquashfsImage(ctx *ConversionContext) error {
	return ctx.runCommand("mksquashfs", ctx.UnpackedPath, ctx.SquashfsPath, "-noappend")
}

func unmountImage(ctx *ConversionContext) error {
	// Always try to unmount and detach, even if one step fails.

	// Unmount the directory first
	if ctx.MountPoint != "" {
		if _, err := os.Stat(ctx.MountPoint); err == nil {
			var umountErr error
			for i := 0; i < 5; i++ {
				umountErr = ctx.runCommand("umount", ctx.MountPoint)
				if umountErr == nil {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if umountErr != nil && !(strings.Contains(umountErr.Error(), "not mounted") || strings.Contains(umountErr.Error(), "not found")) {
				// If umount fails and it's not because it's already unmounted, print a warning.
				fmt.Fprintf(os.Stderr, "\n%s Warning: failed to unmount %s: %v\n", colorize("⚠️", "yellow", ctx.NoColor), ctx.MountPoint, umountErr)
			}
		}
	}

	// Detach the loop device
	if ctx.LoopDevicePath != "" {
		if _, err := os.Stat(ctx.LoopDevicePath); err == nil {
			detachErr := ctx.runCommand("losetup", "-d", ctx.LoopDevicePath)
			if detachErr != nil && !strings.Contains(detachErr.Error(), "No such device") {
				fmt.Fprintf(os.Stderr, "\n%s Warning: failed to detach loop device %s: %v\n", colorize("⚠️", "yellow", ctx.NoColor), ctx.LoopDevicePath, detachErr)
			}
		}
	}

	return nil // Return nil so defer doesn't propagate errors
}