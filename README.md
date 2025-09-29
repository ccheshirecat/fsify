# fsify

Convert Docker images to bootable filesystem images.

## Overview

fsify converts Docker container images into bootable filesystem images that can be used with microVMs, cloud instances, or any environment requiring a bootable disk image. The tool provides a simple command-line interface for converting OCI/Docker images to various filesystem formats.

## Installation

### From Source

```bash
git clone https://github.com/ccheshirecat/fsify.git
cd fsify
go build -o fsify main.go
sudo mv fsify /usr/local/bin/
```

### Requirements

- Root privileges (for mount/mkfs operations)
- Go 1.19+
- skopeo (for pulling OCI images)
- umoci (for unpacking OCI images)
- Core utilities (dd, du, cp, fallocate)
- Filesystem utilities (mkfs.<type>, mount, umount)

### Optional Dependencies

- pv (for progress monitoring during copy operations)
- mksquashfs (for dual-output mode)

## Usage

### Basic Usage

```bash
sudo fsify nginx:latest
```

This creates `nginx-latest.img` in the current directory.

### Advanced Usage

```bash
# Use XFS filesystem with 100MB buffer
sudo fsify -v -fs xfs -s 100 alpine:3.18

# Custom output path
sudo fsify -o my-image.img ubuntu:22.04

# Preallocate disk space
sudo fsify --preallocate nginx:latest

# Generate both ext4 and squashfs images
sudo fsify --dual-output redis:7.0
```

## Command Line Options

```
-h, --help              Show help message
--version               Show version information
-v, --verbose           Enable verbose output
-o, --output FILE       Output file path (default: <image-name>.img)
-q, --quiet             Quiet mode (minimal output)
--no-color              Disable colored output
-fs, --filesystem TYPE  Filesystem type (ext4, xfs, btrfs) (default: ext4)
-s, --size-buffer MB    Extra space in MB to add to the image (default: 50)
--preallocate           Preallocate disk space instead of sparse allocation
--dual-output           Generate both primary filesystem AND squashfs image
```

## Examples

### Production Deployment

```bash
# Create optimized production image with preallocation
sudo fsify --preallocate -fs ext4 -s 200 myapp:production

# Generate both bootable and compressed images
sudo fsify --dual-output database:latest
```

### Development

```bash
# Verbose output for debugging
sudo fsify -v -fs xfs nginx:latest

# Custom output location
sudo fsify -o /mnt/images/webserver.img nginx:stable
```

## Output

By default, fsify creates a bootable filesystem image with the same name as the Docker image tag:

```
nginx:latest → nginx-latest.img
redis:7.0 → redis-7.0.img
```

The output image contains a complete root filesystem extracted from the Docker image, ready to be booted in a virtualized environment.

## Features

- **Cross-filesystem Support**: Automatically handles ext4, XFS, and Btrfs with proper flags
- **OCI Config Embedding**: Preserves Docker container metadata in `/etc/fsify-entrypoint`
- **Dual Output Mode**: Generate both bootable filesystem and compressed squashfs images
- **Progress Monitoring**: Real-time progress bar during file copying operations
- **Resource Management**: Automatic loop device cleanup and resource management
- **Sparse Allocation**: Efficient disk usage with optional preallocation

## Architecture

fsify follows a multi-step process:

1. Download Docker image using skopeo
2. Unpack OCI layers using umoci
3. Extract and preserve OCI configuration
4. Calculate required disk space
5. Create filesystem image
6. Mount and copy files with progress monitoring
7. Generate additional formats (if requested)

## Error Handling

The tool includes comprehensive error handling with helpful hints for common issues:

- Missing filesystem tools (suggests package installation)
- Permission issues (clear privilege requirements)
- Disk space issues (helpful size calculations)

## Contributing

Contributions are welcome. Please ensure all changes maintain the tool's focus on reliability and simplicity.

## License

MIT License - see LICENSE file for details.

## Support

For issues and questions, please use the GitHub issue tracker.