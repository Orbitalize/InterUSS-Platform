{{- define "google-lb-crdb-annotations" -}}
{{- end -}}

{{- define "google-lb-spec" -}}
loadBalancerIP: {{.ip}}
{{- end -}}

{{- define "google-ingress-dss-gateway-annotations" -}}
kubernetes.io/ingress.allow-http: "false"
kubernetes.io/ingress.global-static-ip-name: {{.ip}}
networking.gke.io/managed-certificates: {{.certName}}
networking.gke.io/v1beta1.FrontendConfig: {{.sslPolicy}}
{{- end -}}

{{- define "google-ingress-spec" -}}
{{- end -}}
