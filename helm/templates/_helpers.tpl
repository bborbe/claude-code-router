{{/* Common labels applied to every object. */}}
{{- define "claude-code-router.labels" -}}
app.kubernetes.io/name: claude-code-router
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- with .Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
{{- end -}}

{{/* Image ref: registry/repository:tag, tag defaulting to appVersion. */}}
{{- define "claude-code-router.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository $tag -}}
{{- end -}}

{{/* Name of the object holding the config.yaml (ConfigMap or Secret). */}}
{{- define "claude-code-router.configObject" -}}
{{- if .Values.existingConfigSecret -}}
{{- .Values.existingConfigSecret -}}
{{- else -}}
{{- printf "%s-config" .Release.Name -}}
{{- end -}}
{{- end -}}

{{/* Kind of the config object (ConfigMap or Secret) — decides the volume mount. */}}
{{- define "claude-code-router.configKind" -}}
{{- if .Values.existingConfigSecret -}}
Secret
{{- else -}}
ConfigMap
{{- end -}}
{{- end -}}
