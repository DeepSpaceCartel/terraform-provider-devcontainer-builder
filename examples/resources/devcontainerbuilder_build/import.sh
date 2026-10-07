# Import an already-pushed image by its reference, <registry>/<name>:<tag>.
# The reference is split at its last "/", so the registry may be namespaced
# (ghcr.io/org). For an image name that itself contains "/", use the
# unambiguous <registry>,<name>,<tag> form instead.
#
# Only the image identity comes from the reference. repository, branch and
# image_spec are adopted from configuration on the next apply, without a
# rebuild, and are not checked against the image (an unset branch stays
# empty: which branch the image came from is unknown). git_credentials and
# registry_credentials can never be imported; that same apply stores them
# from configuration. The import itself checks the image exists using the
# service's own (ambient) registry credentials.
terraform import devcontainerbuilder_build.example ghcr.io/example/example-devcontainer:sha-abc1234
