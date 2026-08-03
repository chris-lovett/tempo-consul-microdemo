{{/*
Expand the chart name.
*/}}
{{- define "tempo-consul-microdemo.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified release name, capped at 63 chars.
*/}}
{{- define "tempo-consul-microdemo.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Render a service image reference, optionally prefixing global.imageRegistry.
Usage: {{ include "tempo-consul-microdemo.appImage" (dict "Values" $.Values "repository" $svc.image.repository "tag" $svc.image.tag) }}
*/}}
{{- define "tempo-consul-microdemo.appImage" -}}
{{- $registry := .Values.global.imageRegistry -}}
{{- $repo := .repository -}}
{{- $tag := .tag -}}
{{- if $registry -}}
{{ printf "%s/%s:%s" $registry $repo $tag }}
{{- else -}}
{{ printf "%s:%s" $repo $tag }}
{{- end -}}
{{- end -}}

{{/*
Resolve the health-check port for a waitForDependencies init-container.
Supports "otel-collector" plus any service defined under .Values.services.
*/}}
{{- define "tempo-consul-microdemo.dependencyPort" -}}
{{- $root := .root -}}
{{- $dep := .dep -}}
{{- if eq $dep "otel-collector" -}}
{{- $root.Values.otelCollector.service.httpPort -}}
{{- else if hasKey $root.Values.services $dep -}}
{{- (index $root.Values.services $dep).service.port -}}
{{- else -}}
{{- fail (printf "unknown dependency %q; expected \"otel-collector\" or a key under services" $dep) -}}
{{- end -}}
{{- end -}}
