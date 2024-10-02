# Dashboard Visualization

You can use Grafana to visualize the metrics exported by this exporter. A sample `grafana.json` dashboard configuration is provided here.

To import the dashboard:

- Open your Grafana instance.
- Click on the Plus (+) icon on the left sidebar and select Import.
- Upload the `grafana.json` file or paste its JSON content.
- Select the Prometheus data source you are using to scrape the exporter.