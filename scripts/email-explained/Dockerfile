# The guide's content bundle (bundle.mjs's output) as an OCI image, so the
# website can fetch an exact version of it from ghcr.io with no credentials:
# one layer, holding bundle.json and diagrams/. Nothing runs it.
FROM scratch
COPY . /
