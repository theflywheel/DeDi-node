# Prometheus with the DeDi scrape config baked in.
#
# The config is in the image rather than on the volume on purpose: the previous
# Prometheus in the canvas project was built from a local directory that no
# longer exists anywhere, so its configuration could not be recovered or
# reviewed. Baking it here means the scrape targets and alert rules live in this
# repository, and a redeploy is the only way they change.
FROM prom/prometheus:v3.1.0

COPY prometheus.yml /etc/prometheus/prometheus.yml
COPY rules.yml /etc/prometheus/rules.yml

# Railway attaches the volume owned by root, while this image runs as nobody —
# so Prometheus panics on its first write to /prometheus ("Unable to create
# mmap-ed active query log") and crash-loops. Running as root is the fix that
# does not depend on a chown helper the base image does not ship; the container
# is single-tenant and holds nothing but scrape data.
USER root

# Retention is time-based and modest: this is operational history for alerting,
# not a metrics warehouse, and the volume is sized accordingly.
CMD ["--config.file=/etc/prometheus/prometheus.yml", \
     "--storage.tsdb.path=/prometheus", \
     "--storage.tsdb.retention.time=30d", \
     "--web.listen-address=0.0.0.0:9090", \
     "--web.enable-lifecycle"]
