{{- /* A namespace and pod labels, networkPolicy.dns's or .postgres's with the
namespace filled in, as Cilium's matchLabels and as a Calico selector. */}}
{{- define "kritika.ciliumLabels" -}}
k8s:io.kubernetes.pod.namespace: {{ .namespace | quote }}
{{- range $key, $value := .podSelector }}
{{ $key | quote }}: {{ $value | quote }}
{{- end }}
{{- end -}}
{{- define "kritika.calicoSelector" -}}
{{- $labels := list }}
{{- range $key, $value := . }}
{{- $labels = append $labels (printf "%s == '%s'" $key $value) }}
{{- end }}
{{- join " && " $labels | default "all()" }}
{{- end -}}
{{- /* DNS egress to the cluster's DNS alone, as networkPolicy.dns describes
it, in each flavor: kritika.dnsEgress and kritika.calicoDNSEgress take
networkPolicy.dns, kritika.ciliumDNSEgress a dict of it as dns and of the
names the pods may look up as names, any name when there are none. */}}
{{- define "kritika.dnsEgress" -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: {{ .namespace | quote }}
      podSelector:
        matchLabels:
          {{- toYaml .podSelector | nindent 10 }}
    {{- range .nodeLocalAddresses }}
    - ipBlock:
        cidr: {{ include "kritika.hostCIDR" . | quote }}
    {{- end }}
  ports:
    - port: 53
      protocol: UDP
    - port: 53
      protocol: TCP
{{- end -}}
{{- define "kritika.hostCIDR" -}}
{{ . }}/{{ ternary "128" "32" (contains ":" .) }}
{{- end -}}
{{- define "kritika.ciliumDNSEgress" -}}
- toEndpoints:
    - matchLabels:
        {{- include "kritika.ciliumLabels" .dns | nindent 8 }}
  toPorts:
    {{- include "kritika.ciliumDNSPorts" .names | nindent 4 }}
{{- /* Cilium gives the node's own addresses, a NodeLocal DNSCache's among
them, the host's identity, which no CIDR selects. */}}
{{- if .dns.nodeLocalAddresses }}
- toEntities:
    - host
  toPorts:
    {{- include "kritika.ciliumDNSPorts" .names | nindent 4 }}
{{- end }}
{{- end -}}
{{- define "kritika.ciliumDNSPorts" -}}
- ports:
    - port: "53"
      protocol: UDP
    - port: "53"
      protocol: TCP
  {{- with . }}
  rules:
    dns:
      {{- range . }}
      - {{ ternary "matchPattern" "matchName" (contains "*" .) }}: {{ . | quote }}
      {{- end }}
  {{- end }}
{{- end -}}
{{- define "kritika.calicoDNSEgress" -}}
{{- range $protocol := list "UDP" "TCP" }}
- action: Allow
  protocol: {{ $protocol }}
  destination:
    namespaceSelector: {{ printf "kubernetes.io/metadata.name == '%s'" $.namespace | quote }}
    selector: {{ include "kritika.calicoSelector" $.podSelector | quote }}
    ports:
      - 53
{{- /* One rule an address: a rule's nets may not mix IPv4 and IPv6. */}}
{{- range $.nodeLocalAddresses }}
- action: Allow
  protocol: {{ $protocol }}
  destination:
    nets:
      - {{ include "kritika.hostCIDR" . | quote }}
    ports:
      - 53
{{- end }}
{{- end }}
{{- end -}}
{{- /* The runner pods' NetworkPolicy: no ingress at all; egress to the
database and the gateway port on the server pods, through which the git
remote, the model and similar-code endpoints and every allowed host are
reached, and to the cluster's DNS when withDNS is set. */}}
{{- define "kritika.runnerNetworkPolicy" -}}
{{- $root := .root }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "kritika.fullname" $root }}-runner
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "kritika.labels" $root | nindent 4 }}
    app.kubernetes.io/component: runner
spec:
  podSelector:
    matchLabels:
      kritika.home-operations.com/role: runner
  policyTypes:
    - Ingress
    - Egress
  egress:
    {{- if .withDNS }}
    {{- include "kritika.dnsEgress" .dns | nindent 4 }}
    {{- end }}
    - to:
        {{- if .pgPeer }}
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: {{ .pgPods.namespace | quote }}
          podSelector:
            matchLabels:
              {{- toYaml .pgPods.podSelector | nindent 14 }}
        {{- end }}
        {{- range .pg.cidrs }}
        - ipBlock:
            cidr: {{ . | quote }}
        {{- end }}
      ports:
        - port: {{ .postgres }}
          protocol: TCP
    - to:
        - podSelector:
            matchLabels:
              {{- include "kritika.selectorLabels" $root | nindent 14 }}
      ports:
        - port: {{ .gateway }}
          protocol: TCP
{{- end -}}
{{- if .Values.networkPolicy.enabled }}
{{- $np := .Values.networkPolicy }}
{{- $public := int .Values.service.port }}
{{- $metrics := int .Values.service.metricsPort }}
{{- $gateway := int .Values.service.gatewayPort }}
{{- $postgres := int $np.postgres.port }}
{{- $serverPorts := concat $np.egressPorts (list $postgres) }}
{{- if and .Values.gatewayCache.enabled (eq .Values.gatewayCache.storage.type "s3") }}
{{- $endpoint := urlParse (tpl .Values.gatewayCache.storage.endpoint .) }}
{{- $port := trimPrefix ":" (regexFind ":[0-9]+$" $endpoint.host) | default (ternary "443" "80" (eq $endpoint.scheme "https")) }}
{{- $serverPorts = append $serverPorts (int $port) | uniq }}
{{- end }}
{{- $serverSelector := printf "app.kubernetes.io/name == '%s' && app.kubernetes.io/instance == '%s'" (include "kritika.name" .) .Release.Name }}
{{- $dns := dict "namespace" $np.dns.namespace "podSelector" ($np.dns.podSelector | default (dict "k8s-app" "kube-dns")) "nodeLocalAddresses" $np.dns.nodeLocalAddresses }}
{{- $pg := $np.postgres }}
{{- $pgPods := dict "namespace" ($pg.namespace | default .Release.Namespace) "podSelector" $pg.podSelector }}
{{- /* The database's pods are a peer unless cidrs alone say where it is. */}}
{{- $pgPeer := or $pg.podSelector $pg.namespace (not $pg.cidrs) }}
{{- /* auto renders the flavor whose API the cluster serves, as Helm, Flux
and Argo CD see it; helm template without --api-versions sees neither and
renders the default flavor. */}}
{{- $type := $np.type }}
{{- if eq $type "auto" }}
{{- $type = "default" }}
{{- if .Capabilities.APIVersions.Has "cilium.io/v2/CiliumNetworkPolicy" }}
{{- $type = "cilium" }}
{{- else if .Capabilities.APIVersions.Has "projectcalico.org/v3/NetworkPolicy" }}
{{- $type = "calico" }}
{{- end }}
{{- end }}
{{- $runner := dict "root" . "dns" $dns "withDNS" true "pg" $pg "pgPods" $pgPods "pgPeer" $pgPeer "postgres" $postgres "gateway" $gateway }}
{{- if eq $type "default" }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "kritika.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "kritika.selectorLabels" . | nindent 6 }}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    # No `from`: the public and metrics ports are reachable by any peer;
    # lock down per cluster with your own policy if needed.
    - ports:
        - port: {{ $public }}
          protocol: TCP
        - port: {{ $metrics }}
          protocol: TCP
    # The gateway is for runner pods alone.
    - from:
        - podSelector:
            matchLabels:
              kritika.home-operations.com/role: runner
      ports:
        - port: {{ $gateway }}
          protocol: TCP
  egress:
    {{- include "kritika.dnsEgress" $dns | nindent 4 }}
    - ports:
        {{- range $serverPorts }}
        - port: {{ . }}
          protocol: TCP
        {{- end }}
    # The API server, to create and watch runner Jobs.
    - ports:
        - port: 443
          protocol: TCP
        - port: 6443
          protocol: TCP
---
{{ include "kritika.runnerNetworkPolicy" $runner }}
{{- else if eq $type "cilium" }}
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: {{ include "kritika.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  endpointSelector:
    matchLabels:
      {{- include "kritika.selectorLabels" . | nindent 6 }}
  ingress:
    - fromEntities:
        - all
      toPorts:
        - ports:
            - port: {{ $public | quote }}
              protocol: TCP
            - port: {{ $metrics | quote }}
              protocol: TCP
    - fromEndpoints:
        - matchLabels:
            kritika.home-operations.com/role: runner
      toPorts:
        - ports:
            - port: {{ $gateway | quote }}
              protocol: TCP
  egress:
    {{- include "kritika.ciliumDNSEgress" (dict "dns" $dns) | nindent 4 }}
    - toEntities:
        - cluster
        - world
      toPorts:
        - ports:
            {{- range $serverPorts }}
            - port: {{ . | quote }}
              protocol: TCP
            {{- end }}
    - toEntities:
        - kube-apiserver
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
            - port: "6443"
              protocol: TCP
---
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: {{ include "kritika.fullname" . }}-runner
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
    app.kubernetes.io/component: runner
spec:
  endpointSelector:
    matchLabels:
      kritika.home-operations.com/role: runner
  # An ingress section with one empty rule puts the pods into default deny
  # for ingress without allowing any peer.
  ingress:
    - {}
  egress:
    {{- /* The cluster's DNS answers its Services' names itself, but for an
    ExternalName Service's, whose lookup goes on to the name its author set. */}}
    {{- $names := prepend $np.dns.runnerNames "*.*.svc.cluster.local" }}
    {{- include "kritika.ciliumDNSEgress" (dict "dns" $dns "names" $names) | nindent 4 }}
    {{- if $pgPeer }}
    - toEndpoints:
        - matchLabels:
            {{- include "kritika.ciliumLabels" $pgPods | nindent 12 }}
      toPorts:
        - ports:
            - port: {{ $postgres | quote }}
              protocol: TCP
    {{- end }}
    {{- with $pg.cidrs }}
    - toCIDR:
        {{- toYaml . | nindent 8 }}
      toPorts:
        - ports:
            - port: {{ $postgres | quote }}
              protocol: TCP
    {{- end }}
    - toEndpoints:
        - matchLabels:
            {{- include "kritika.selectorLabels" . | nindent 12 }}
      toPorts:
        - ports:
            - port: {{ $gateway | quote }}
              protocol: TCP
---
{{- /* Cilium refuses the policy above where its L7 proxy is off, for its
DNS rules, and a runner pod with no policy reaches anything. This one,
without DNS, keeps runner pods to the database and the gateway either way:
it allows nothing the policy above does not, and where that policy is
refused a runner cannot look up a name, so every run fails rather than run
unconfined. */}}
{{- $_ := set $runner "withDNS" false }}
{{ include "kritika.runnerNetworkPolicy" $runner }}
{{- else if eq $type "calico" }}
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: {{ include "kritika.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
spec:
  selector: {{ $serverSelector }}
  types:
    - Ingress
    - Egress
  ingress:
    - action: Allow
      protocol: TCP
      destination:
        ports:
          - {{ $public }}
          - {{ $metrics }}
    - action: Allow
      protocol: TCP
      source:
        selector: kritika.home-operations.com/role == 'runner'
      destination:
        ports:
          - {{ $gateway }}
  egress:
    {{- include "kritika.calicoDNSEgress" $dns | nindent 4 }}
    - action: Allow
      protocol: TCP
      destination:
        ports:
          {{- range $serverPorts }}
          - {{ . }}
          {{- end }}
    - action: Allow
      protocol: TCP
      destination:
        ports:
          - 443
          - 6443
---
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: {{ include "kritika.fullname" . }}-runner
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "kritika.labels" . | nindent 4 }}
    app.kubernetes.io/component: runner
spec:
  selector: kritika.home-operations.com/role == 'runner'
  # Ingress in types with no ingress rules: nothing may reach a runner pod.
  types:
    - Ingress
    - Egress
  egress:
    {{- include "kritika.calicoDNSEgress" $dns | nindent 4 }}
    {{- if $pgPeer }}
    - action: Allow
      protocol: TCP
      destination:
        namespaceSelector: {{ printf "kubernetes.io/metadata.name == '%s'" $pgPods.namespace | quote }}
        selector: {{ include "kritika.calicoSelector" $pgPods.podSelector | quote }}
        ports:
          - {{ $postgres }}
    {{- end }}
    {{- /* One rule a CIDR: a rule's nets may not mix IPv4 and IPv6. */}}
    {{- range $pg.cidrs }}
    - action: Allow
      protocol: TCP
      destination:
        nets:
          - {{ . | quote }}
        ports:
          - {{ $postgres }}
    {{- end }}
    - action: Allow
      protocol: TCP
      destination:
        selector: {{ $serverSelector | quote }}
        ports:
          - {{ $gateway }}
{{- else }}
{{- fail (printf "networkPolicy.type must be one of: auto, default, cilium, calico (got %q)" $np.type) }}
{{- end }}
{{- end }}
