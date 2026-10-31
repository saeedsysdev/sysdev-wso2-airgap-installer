package deploymentassets

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

type DeferredBinding struct {
	Name            string `json:"name"`
	Resource        string `json:"resource"`
	Key             string `json:"key"`
	Producer        string `json:"producer"`
	ResolutionPhase string `json:"resolution_phase"`
}
type Result struct {
	Files    map[string][]byte
	Bindings []DeferredBinding
	Deferred []string
	Blockers []string
}
type Input struct {
	Config          config.File
	Namespace       string
	SelectedWorkers []string
	StorageClass    string
}

// Render independently authors Kubernetes objects and versioned product
// configuration. It contains Secret references, never secret values.
func Render(in Input) (Result, error) {
	ns := in.Namespace
	if ns == "" {
		ns = in.Config.Kubernetes.Namespace
	}
	if ns == "" {
		return Result{}, fmt.Errorf("Kubernetes namespace is required for deployment assets")
	}
	domain := in.Config.Deployment.ClusterDNSDomain
	if domain == "" {
		domain = "cluster.local"
	}
	certs := in.Config.Certificates
	if certs.APIMHostname == "" || certs.GatewayHostname == "" || certs.KeycloakHostname == "" {
		return Result{}, fmt.Errorf("APIM, gateway, and Keycloak hostnames are required to render deployment assets")
	}
	hosts := map[string]string{"apim": certs.APIMHostname, "gateway": certs.GatewayHostname, "keycloak": certs.KeycloakHostname}
	images := map[string]string{}
	for _, i := range registry.Profile.Images {
		ref := in.Config.Registry.Host
		if in.Config.Registry.Project != "" {
			ref += "/" + in.Config.Registry.Project
		}
		ref += "/" + i.Repository + ":" + i.Tag
		switch i.Component {
		case "API Control Plane and resident Key Manager":
			images["acp"] = ref
		case "Keycloak":
			images["keycloak"] = ref
		case "Traffic Manager":
			images["tm"] = ref
		case "Universal Gateway":
			images["gateway"] = ref
		}
	}
	if images["acp"] == "" || images["keycloak"] == "" || images["tm"] == "" || images["gateway"] == "" {
		return Result{}, fmt.Errorf("pinned image mappings are incomplete")
	}
	components := []component{
		{name: "apim-cp", role: "API Control Plane", image: images["acp"], replicas: 2, port: 9443, externalPort: nodePort(0), runtime: "wso2", profileIndex: 0, hostname: hosts["apim"]},
		{name: "apim-km", role: "Separated resident WSO2 Key Manager", image: images["acp"], replicas: 2, port: 9443, runtime: "keymanager", profileIndex: 1, hostname: "apim-km." + ns + ".svc." + domain},
		{name: "apim-tm-1", role: "Traffic Manager instance 1", image: images["tm"], replicas: 1, port: 9443, runtime: "tm", profileIndex: 2, hostname: "apim-tm-1." + ns + ".svc." + domain},
		{name: "apim-tm-2", role: "Traffic Manager instance 2", image: images["tm"], replicas: 1, port: 9443, runtime: "tm", profileIndex: 2, hostname: "apim-tm-2." + ns + ".svc." + domain},
		{name: "apim-gateway", role: "Universal Gateway", image: images["gateway"], replicas: 2, port: 9443, runtime: "gateway", profileIndex: 3, hostname: hosts["gateway"]},
		{name: "keycloak", role: "Keycloak", image: images["keycloak"], replicas: 3, port: 8443, externalPort: nodePort(2), runtime: "keycloak", profileIndex: 4, hostname: hosts["keycloak"]},
	}
	components[0].externalPort = nodePort(0)
	components[4].externalPort = nodePort(1)
	var docs []any
	files := map[string][]byte{}
	for _, c := range components {
		requests := registry.Profile.Components[c.profileIndex]
		resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
		cpuReq, _ := resource.ParseQuantity(requests.CPURequest)
		memReq, _ := resource.ParseQuantity(requests.MemoryRequest)
		memLimit, _ := resource.ParseQuantity(requests.MemoryLimit)
		resources.Requests[corev1.ResourceCPU] = cpuReq
		resources.Requests[corev1.ResourceMemory] = memReq
		resources.Limits[corev1.ResourceMemory] = memLimit
		configName := c.name + "-config"
		configText := renderWSO2Config(in, c, ns, domain, hosts)
		if c.runtime == "keycloak" {
			configText = renderKeycloakEnv(in, c, ns, domain)
		}
		cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: ns, Labels: labels(c.name)}, Data: map[string]string{configFile(c): configText}}
		docs = append(docs, cm)
		servicePorts := ports(c)
		headless := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: c.name + "-headless", Namespace: ns, Labels: labels(c.name)}, Spec: corev1.ServiceSpec{ClusterIP: "None", Selector: labels(c.name), PublishNotReadyAddresses: true, Ports: servicePorts}}
		docs = append(docs, headless)
		serviceType := corev1.ServiceTypeClusterIP
		if c.externalPort > 0 {
			serviceType = corev1.ServiceTypeNodePort
		}
		svcPorts := externalPorts(c)
		svc := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: c.name, Namespace: ns, Labels: labels(c.name)}, Spec: corev1.ServiceSpec{Type: serviceType, Selector: labels(c.name), Ports: svcPorts}}
		docs = append(docs, svc)
		podSpec := makePodSpec(in, c, resources, configName, ns, domain)
		stateful := &appsv1.StatefulSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}, ObjectMeta: metav1.ObjectMeta{Name: c.name, Namespace: ns, Labels: labels(c.name)}, Spec: appsv1.StatefulSetSpec{ServiceName: c.name + "-headless", Replicas: int32ptr(c.replicas), Selector: &metav1.LabelSelector{MatchLabels: labels(c.name)}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(c.name)}, Spec: podSpec}}}
		docs = append(docs, stateful)
		pdb := &policyv1.PodDisruptionBudget{TypeMeta: metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"}, ObjectMeta: metav1.ObjectMeta{Name: c.name, Namespace: ns, Labels: labels(c.name)}, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: intstrPtr(intstr.FromInt32(max(1, c.replicas-1))), Selector: &metav1.LabelSelector{MatchLabels: labels(c.name)}}}
		docs = append(docs, pdb)
		if c.runtime != "keycloak" {
			files["configs/"+c.name+"/deployment.toml"] = []byte(configText)
		}
		files["configs/"+c.name+"/README.md"] = []byte("Generated for WSO2 API Manager " + registry.Profile.WSO2 + " candidate profile. Secrets are referenced through Kubernetes Secret values and WSO2 `$env{...}` expansion. This independently authored configuration requires exact-distribution validation before Apply.\n")
	}
	var b bytes.Buffer
	for i, obj := range docs {
		data, err := yaml.Marshal(obj)
		if err != nil {
			return Result{}, fmt.Errorf("render Kubernetes object: %w", err)
		}
		if i > 0 {
			b.WriteString("---\n")
		}
		b.Write(data)
	}
	files["kubernetes/resources.yaml"] = b.Bytes()
	files["urls.txt"] = []byte(fmt.Sprintf("API Control Plane: https://%s\nUniversal Gateway: https://%s\nKeycloak: https://%s\nInternal WSO2 KM: https://apim-km.%s.svc.%s:9443/services/\nTraffic Managers: apim-tm-1.%s.svc.%s:9611/9711 and apim-tm-2.%s.svc.%s:9611/9711\n", hosts["apim"], hosts["gateway"], hosts["keycloak"], ns, domain, ns, domain, ns, domain))
	files["STAGE4-ORDER.md"] = []byte("# Stage 4 execution prerequisites\n\nThese are preparation assets, not an Apply bundle. Resolve all typed Secret bindings from protected state only after explicit Apply approval. Verify Oracle reachability from the Kubernetes network before dependent operations. If Keycloak's schema is uninitialized, start exactly one Keycloak instance using its supported built-in Oracle migration, verify readiness, then scale to the configured clustered count. Do not run a handwritten Keycloak schema script or start multiple initializers concurrently. Verify the custom Keycloak image contains the supported Oracle JDBC and localization driver artifacts before startup.\n")
	res := Result{Files: files, Bindings: []DeferredBinding{
		{Name: "Registry pull credentials", Resource: "Secret/registry-pull", Key: ".dockerconfigjson", Producer: "protected registry credential provider", ResolutionPhase: "Stage 4 pre-Apply secret materialization"},
		{Name: "Oracle runtime passwords", Resource: "Secret/oracle-runtime", Key: "APIM/SHARED/KEYCLOAK password keys", Producer: "protected Oracle credential provider", ResolutionPhase: "Stage 4 pre-Apply secret materialization"},
		{Name: "WSO2 shared encryption key", Resource: "Secret/wso2-runtime", Key: "APIM_ENCRYPTION_KEY", Producer: "protected per-installation secret store", ResolutionPhase: "Stage 4 pre-Apply secret materialization"},
		{Name: "Workload private keys and JKS stores", Resource: "Secret/wso2-keystores-* and Secret/keycloak-tls", Key: "wso2carbon.jks, tls.jks, client-truststore.jks, tls.crt, tls.key", Producer: "protected PKI material store; private keys never enter public export", ResolutionPhase: "Stage 4 pre-Apply secret materialization"},
		{Name: "Keycloak JDBC driver", Resource: "custom Keycloak image", Key: "ojdbc17 and orai18n", Producer: "verified offline Oracle JDBC artifacts", ResolutionPhase: "Stage 4 image build/package"},
	}}
	if in.Config.Oracle.SchemaMode == "initialize" && in.Config.Artifacts.SchemaManifestPath == "" {
		res.Blockers = append(res.Blockers, "Oracle initialization selected but no verified local WSO2 schema artifact manifest was supplied.")
	}
	if !in.Config.DistributedThrottle.ReplicationMode {
		res.Blockers = append(res.Blockers, "APIM 4.7 distributed throttling requires an external replicated Redis/Valkey store; no supported replicated endpoint is configured.")
	}
	res.Blockers = append(res.Blockers, "Exact-release WSO2 and Keycloak asset/runtime compatibility is unqualified; verify product parsing, required JDBC artifacts, ports, TLS aliases, and the Keycloak single-instance initialization sequence before Apply.")
	res.Deferred = append(res.Deferred, "Keycloak realm/client and external WSO2 Key Manager registration are Stage 5; generated URLs are stable inputs only.", "Oracle service is outside Kubernetes; workstation reachability does not establish in-cluster reachability. Stage 4 must run the deferred post-Apply connectivity check before workloads.", "No PVC is generated: configured product state is in Oracle and generated certificates/configuration are supplied by protected Secret references. Verify this storage profile against exact offline distributions.", "NodePort checks depend on live Kubernetes services; this file does not claim ports are conflict-free.")
	return res, nil
}

type component struct {
	name, role, image, runtime, hostname string
	replicas                             int32
	port, externalPort, profileIndex     int
}

func ports(c component) []corev1.ServicePort {
	switch c.runtime {
	case "tm":
		return []corev1.ServicePort{{Name: "management-https", Port: 9443, TargetPort: intstr.FromInt32(9443)}, {Name: "throttle-binary", Port: 9611, TargetPort: intstr.FromInt32(9611)}, {Name: "throttle-auth", Port: 9711, TargetPort: intstr.FromInt32(9711)}, {Name: "event-jms", Port: 5672, TargetPort: intstr.FromInt32(5672)}}
	case "gateway":
		return []corev1.ServicePort{{Name: "management-https", Port: 9443, TargetPort: intstr.FromInt32(9443)}, {Name: "gateway-https", Port: 8243, TargetPort: intstr.FromInt32(8243)}, {Name: "gateway-http", Port: 8280, TargetPort: intstr.FromInt32(8280)}, {Name: "event-jms", Port: 5672, TargetPort: intstr.FromInt32(5672)}}
	case "keycloak":
		return []corev1.ServicePort{{Name: "https", Port: 8443, TargetPort: intstr.FromInt32(8443)}, {Name: "health", Port: 9000, TargetPort: intstr.FromInt32(9000)}}
	case "wso2", "keymanager":
		return []corev1.ServicePort{{Name: "management-https", Port: 9443, TargetPort: intstr.FromInt32(9443)}, {Name: "event-jms", Port: 5672, TargetPort: intstr.FromInt32(5672)}}
	default:
		return []corev1.ServicePort{{Name: "management-https", Port: 9443, TargetPort: intstr.FromInt32(9443)}, {Name: "event-jms", Port: 5672, TargetPort: intstr.FromInt32(5672)}}
	}
}
func externalPorts(c component) []corev1.ServicePort {
	if c.externalPort == 0 {
		return ports(c)
	}
	target := int32(c.port)
	name := "https"
	if c.runtime == "gateway" {
		target = 8243
		name = "gateway-https"
	}
	return []corev1.ServicePort{{Name: name, Port: target, TargetPort: intstr.FromInt32(target), NodePort: int32(c.externalPort), Protocol: corev1.ProtocolTCP}}
}
func nodePort(i int) int {
	p := registry.Profile.NodePorts
	if i >= 0 && i < len(p) {
		return p[i].Port
	}
	return 0
}
func labels(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name, "app.kubernetes.io/part-of": "sysdev-wso2-airgap-installer", "app.kubernetes.io/version": "0.1.0-dev"}
}
func int32ptr(v int32) *int32                            { return &v }
func intstrPtr(v intstr.IntOrString) *intstr.IntOrString { return &v }
func makePodSpec(in Input, c component, resources corev1.ResourceRequirements, configName, ns, domain string) corev1.PodSpec {
	serviceName := "wso2-keystores-" + c.name
	keystoreMount := strings.TrimSuffix(configPath(c), "conf/deployment.toml") + "resources/security"
	if c.runtime == "keycloak" {
		serviceName = "keycloak-tls"
		keystoreMount = "/opt/keycloak/conf/tls"
	}
	container := corev1.Container{
		Name: c.name, Image: c.image, ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: containerPorts(c), Resources: resources, Env: environment(c),
		SecurityContext: &corev1.SecurityContext{RunAsNonRoot: boolPtr(true), AllowPrivilegeEscalation: boolPtr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		VolumeMounts:    []corev1.VolumeMount{{Name: "product-config", MountPath: configPath(c), SubPath: configFile(c), ReadOnly: true}, {Name: "runtime-keystores", MountPath: keystoreMount, ReadOnly: true}},
	}
	if c.runtime == "keycloak" {
		container.Command = []string{"/opt/keycloak/bin/kc.sh"}
		container.Args = []string{"start", "--optimized"}
		container.StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health/started", Port: intstr.FromInt32(9000), Scheme: corev1.URISchemeHTTP}}, PeriodSeconds: 10, FailureThreshold: 60}
		container.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health/ready", Port: intstr.FromInt32(9000), Scheme: corev1.URISchemeHTTP}}, PeriodSeconds: 10, FailureThreshold: 6}
		container.LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health/live", Port: intstr.FromInt32(9000), Scheme: corev1.URISchemeHTTP}}, PeriodSeconds: 20, FailureThreshold: 3}
	} else {
		container.StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(9443)}}, PeriodSeconds: 10, FailureThreshold: 60}
		container.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(9443)}}, PeriodSeconds: 10, FailureThreshold: 6}
		container.LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(9443)}}, PeriodSeconds: 20, FailureThreshold: 3}
	}
	volumes := []corev1.Volume{
		{Name: "product-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}},
		{Name: "runtime-keystores", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: serviceName, Optional: boolPtr(false)}}},
	}
	var affinity *corev1.Affinity
	if workers := append([]string(nil), in.SelectedWorkers...); len(workers) > 0 {
		sort.Strings(workers)
		affinity = &corev1.Affinity{
			NodeAffinity:    &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: workers}}}}}},
			PodAntiAffinity: &corev1.PodAntiAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{Weight: 100, PodAffinityTerm: corev1.PodAffinityTerm{TopologyKey: "kubernetes.io/hostname", LabelSelector: &metav1.LabelSelector{MatchLabels: labels(c.name)}}}}},
		}
	}
	return corev1.PodSpec{Containers: []corev1.Container{container}, Volumes: volumes, Affinity: affinity, TerminationGracePeriodSeconds: int64Ptr(60), ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-pull"}}}
}
func environment(c component) []corev1.EnvVar {
	if c.runtime == "keycloak" {
		return []corev1.EnvVar{{Name: "KC_DB", Value: "oracle"}, {Name: "KC_DB_URL", ValueFrom: secretKey("oracle-runtime", "KEYCLOAK_JDBC_URL")}, {Name: "KC_DB_USERNAME", ValueFrom: secretKey("oracle-runtime", "KEYCLOAK_USERNAME")}, {Name: "KC_DB_PASSWORD", ValueFrom: secretKey("oracle-runtime", "KEYCLOAK_PASSWORD")}, {Name: "KC_HOSTNAME_STRICT", Value: "true"}, {Name: "KC_HOSTNAME", Value: "https://" + c.hostname}, {Name: "KC_HEALTH_ENABLED", Value: "true"}, {Name: "KC_METRICS_ENABLED", Value: "true"}, {Name: "KC_HTTP_ENABLED", Value: "false"}, {Name: "KC_HTTPS_CERTIFICATE_FILE", Value: "/opt/keycloak/conf/tls/tls.crt"}, {Name: "KC_HTTPS_CERTIFICATE_KEY_FILE", Value: "/opt/keycloak/conf/tls/tls.key"}}
	}
	return []corev1.EnvVar{{Name: "APIM_ENCRYPTION_KEY", ValueFrom: secretKey("wso2-runtime", "APIM_ENCRYPTION_KEY")}, {Name: "WSO2_APIM_JDBC_URL", ValueFrom: secretKey("oracle-runtime", "WSO2_APIM_JDBC_URL")}, {Name: "WSO2_APIM_DB_PASSWORD", ValueFrom: secretKey("oracle-runtime", "WSO2_APIM_PASSWORD")}, {Name: "WSO2_SHARED_JDBC_URL", ValueFrom: secretKey("oracle-runtime", "WSO2_SHARED_JDBC_URL")}, {Name: "WSO2_SHARED_DB_PASSWORD", ValueFrom: secretKey("oracle-runtime", "WSO2_SHARED_PASSWORD")}, {Name: "WSO2_SUPER_ADMIN_USERNAME", ValueFrom: secretKey("wso2-runtime", "WSO2_SUPER_ADMIN_USERNAME")}, {Name: "WSO2_SUPER_ADMIN_PASSWORD", ValueFrom: secretKey("wso2-runtime", "WSO2_SUPER_ADMIN_PASSWORD")}, {Name: "WSO2_KM_USERNAME", ValueFrom: secretKey("wso2-runtime", "WSO2_KM_USERNAME")}, {Name: "WSO2_KM_PASSWORD", ValueFrom: secretKey("wso2-runtime", "WSO2_KM_PASSWORD")}, {Name: "WSO2_KEYSTORE_PASSWORD", ValueFrom: secretKey("wso2-runtime", "WSO2_KEYSTORE_PASSWORD")}, {Name: "WSO2_TLS_KEYSTORE_PASSWORD", ValueFrom: secretKey("wso2-runtime", "WSO2_TLS_KEYSTORE_PASSWORD")}, {Name: "WSO2_TRUSTSTORE_PASSWORD", ValueFrom: secretKey("wso2-runtime", "WSO2_TRUSTSTORE_PASSWORD")}, {Name: "DISTRIBUTED_THROTTLE_USERNAME", ValueFrom: secretKey("distributed-throttle", "username")}, {Name: "DISTRIBUTED_THROTTLE_PASSWORD", ValueFrom: secretKey("distributed-throttle", "password")}}
}
func secretKey(secret, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}}
}
func containerPorts(c component) []corev1.ContainerPort {
	out := []corev1.ContainerPort{}
	for _, p := range ports(c) {
		out = append(out, corev1.ContainerPort{Name: p.Name, ContainerPort: p.Port, Protocol: corev1.ProtocolTCP})
	}
	return out
}
func configFile(c component) string {
	if c.runtime == "keycloak" {
		return "keycloak.conf"
	}
	return "deployment.toml"
}
func configPath(c component) string {
	if c.runtime == "keycloak" {
		return "/opt/keycloak/conf/keycloak.conf"
	}
	suffix := "wso2am-acp"
	if c.runtime == "tm" {
		suffix = "wso2am-tm"
	}
	if c.runtime == "gateway" {
		suffix = "wso2am-universal-gw"
	}
	return "/home/wso2carbon/" + suffix + "-4.7.0/repository/conf/deployment.toml"
}
func boolPtr(v bool) *bool    { return &v }
func int64Ptr(v int64) *int64 { return &v }

func renderWSO2Config(in Input, c component, ns, domain string, hosts map[string]string) string {
	tm1 := "apim-tm-1." + ns + ".svc." + domain
	tm2 := "apim-tm-2." + ns + ".svc." + domain
	cp0 := "apim-cp-0.apim-cp-headless." + ns + ".svc." + domain
	cp1 := "apim-cp-1.apim-cp-headless." + ns + ".svc." + domain
	var b strings.Builder
	fmt.Fprintf(&b, "[server]\nhostname = %s\n", strconv.Quote(c.hostname))
	fmt.Fprintf(&b, "\n[encryption]\nkey = \"$env{APIM_ENCRYPTION_KEY}\"\n")
	b.WriteString("\n[keystore.primary]\nfile_name = \"wso2carbon.jks\"\ntype = \"JKS\"\npassword = \"$env{WSO2_KEYSTORE_PASSWORD}\"\nalias = \"wso2carbon\"\nkey_password = \"$env{WSO2_KEYSTORE_PASSWORD}\"\n")
	b.WriteString("\n[keystore.tls]\nfile_name = \"tls.jks\"\ntype = \"JKS\"\npassword = \"$env{WSO2_TLS_KEYSTORE_PASSWORD}\"\nalias = \"wso2carbon\"\nkey_password = \"$env{WSO2_TLS_KEYSTORE_PASSWORD}\"\n")
	b.WriteString("\n[truststore]\nfile_name = \"client-truststore.jks\"\ntype = \"JKS\"\npassword = \"$env{WSO2_TRUSTSTORE_PASSWORD}\"\n")
	fmt.Fprintf(&b, "\n[database.apim_db]\ntype = \"oracle\"\nurl = \"$env{WSO2_APIM_JDBC_URL}\"\nusername = %s\npassword = \"$env{WSO2_APIM_DB_PASSWORD}\"\ndriver = \"oracle.jdbc.OracleDriver\"\n", strconv.Quote(in.Config.Oracle.APIMRuntimeAccount))
	fmt.Fprintf(&b, "\n[database.shared_db]\ntype = \"oracle\"\nurl = \"$env{WSO2_SHARED_JDBC_URL}\"\nusername = %s\npassword = \"$env{WSO2_SHARED_DB_PASSWORD}\"\ndriver = \"oracle.jdbc.OracleDriver\"\n", strconv.Quote(in.Config.Oracle.SharedRuntimeAccount))
	if c.runtime == "wso2" {
		fmt.Fprintf(&b, "\n[apim.key_manager]\nservice_url = %s\nusername = \"$env{WSO2_KM_USERNAME}\"\npassword = \"$env{WSO2_KM_PASSWORD}\"\n", strconv.Quote("https://apim-km."+ns+".svc."+domain+":9443/services/"))
	}
	if c.runtime == "tm" || c.runtime == "gateway" || c.runtime == "wso2" || c.runtime == "keymanager" {
		fmt.Fprintf(&b, "\n[apim.event_hub]\nenable = true\nusername = \"$env{WSO2_SUPER_ADMIN_USERNAME}\"\npassword = \"$env{WSO2_SUPER_ADMIN_PASSWORD}\"\nservice_url = %s\nevent_listening_endpoints = [%s, %s]\nevent_duplicate_url = [%s]\n", strconv.Quote("https://apim-cp."+ns+".svc."+domain+":9443/services/"), strconv.Quote("tcp://"+cp0+":5672"), strconv.Quote("tcp://"+cp1+":5672"), strconv.Quote("tcp://"+cp1+":5672"))
		fmt.Fprintf(&b, "\n[apim.throttling]\nthrottle_decision_endpoints = [%s, %s]\n", strconv.Quote("tcp://"+tm1+":5672"), strconv.Quote("tcp://"+tm2+":5672"))
		fmt.Fprintf(&b, "\n[[apim.throttling.url_group]]\ntraffic_manager_urls = [%s, %s]\ntraffic_manager_auth_urls = [%s, %s]\ntype = \"loadbalance\"\n", strconv.Quote("tcp://"+tm1+":9611"), strconv.Quote("tcp://"+tm2+":9611"), strconv.Quote("ssl://"+tm1+":9711"), strconv.Quote("ssl://"+tm2+":9711"))
	}
	if in.Config.DistributedThrottle.ReplicationMode {
		d := in.Config.DistributedThrottle
		fmt.Fprintf(&b, "\n[apim.distributed_throttling]\nenable = true\ntype = %s\nsync_interval = 1\ncore_pool_size = 5\n\n[apim.distributed_throttling.keyvalue_store]\nhost = %s\nport = %d\nuser = \"$env{DISTRIBUTED_THROTTLE_USERNAME}\"\npassword = \"$env{DISTRIBUTED_THROTTLE_PASSWORD}\"\n", strconv.Quote(d.Type), strconv.Quote(d.Host), d.Port)
	} else {
		b.WriteString("\n[apim.distributed_throttling]\nenable = false\n")
	}
	return b.String()
}

func renderKeycloakEnv(in Input, c component, ns, domain string) string {
	_ = in
	_ = ns
	_ = domain
	return "# Keycloak 26.8.1 settings are supplied through typed environment Secret references in the StatefulSet.\n"
}
