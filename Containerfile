# Allow build scripts to be referenced without being copied into the final image
FROM scratch AS ctx

COPY base_image/files/system /system_files/
COPY base_image/files/scripts /build_files/
COPY base_image/*.pub /keys/

# Base Image
FROM quay.io/almalinuxorg/almalinux-bootc:10@sha256:d8679e022ff2b9f9873becf262e4447beb5b0551a8dc83c146e2e6f27bd5183f

ARG IMAGE_NAME=diva-os
ARG IMAGE_REGISTRY=localhost
ARG VARIANT=kde
ARG TARGETARCH

# Note: /opt is NOT mounted as tmpfs so /opt/diva/venv persists in the immutable rootfs
RUN --mount=type=tmpfs,dst=/tmp \
    --mount=type=bind,from=ctx,source=/,target=/ctx \
    /ctx/build_files/build.sh

### LINTING
## Verify final image and contents are correct.
RUN bootc container lint
