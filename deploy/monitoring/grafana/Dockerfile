# Grafana with the Prometheus datasource and the node dashboard provisioned.
#
# Nothing here depends on the volume surviving. The previous Grafana kept its
# dashboards, users and datasources on a disk that cannot move between Railway
# projects, which is why they had to be rebuilt; provisioning from the image
# means a rebuild is a redeploy.
FROM grafana/grafana:11.5.1

# Railway attaches the volume owned by root, while this image runs as uid 472 —
# so Grafana cannot create /var/lib/grafana/plugins and crash-loops on
# "GF_PATHS_DATA is not writable". Same reason as the Prometheus image.
USER root

COPY datasource.yml  /etc/grafana/provisioning/datasources/dedi.yml
COPY dashboards.yml  /etc/grafana/provisioning/dashboards/dedi.yml
COPY dashboard.json  /etc/grafana/provisioning/dashboards/dedi-nodes.json
