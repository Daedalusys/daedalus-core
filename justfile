# Default recipe: list available recipes
default:
    @just --list

# Sync Daedalus-owned files into base_image tree
sync:
    ./sync-daedalus.sh

# Build Daedalus container image
build: sync
    podman build --platform=linux/amd64 --security-opt=label=disable --cap-add=all --device /dev/fuse --build-arg IMAGE_NAME=daedalus-os --build-arg IMAGE_REGISTRY=localhost --build-arg VARIANT=kde -t localhost/daedalus-os:latest -f Containerfile .

# Run test suite
test:
    deno test --allow-all daedalus/files/system/opt/daedalus/deno/
    python3 -m pytest tests/

# Build bootable ISO
iso:
    mkdir -p output
    podman run --rm -v "$(pwd)/output":/output --security-opt label=disable quay.io/centos-bootc/bootc-image-builder:latest --type iso --image-name localhost/daedalus-os:latest

# Build qcow2 and run in QEMU
qemu:
    mkdir -p output
    podman run --rm -v "$(pwd)/output":/output --security-opt label=disable quay.io/centos-bootc/bootc-image-builder:latest --type qcow2 --image-name localhost/daedalus-os:latest
    qemu-system-x86_64 -m 4096 -cdrom output/boot.qcow2 -enable-kvm -vga virtio
