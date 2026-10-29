//go:build e2e
// +build e2e

/*
Copyright 2026 The rook-mon-dns Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rook-mon-dns/rook-mon-dns/test/utils"
)

// namespace is where Rook usually lives and where the operator is deployed
// by config/default.
const namespace = "rook-ceph"

// monEndpointsConfigMap mimics the ConfigMap Rook writes (hostNetwork mons:
// the IPs in data equal the node addresses in mapping).
const monEndpointsConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: rook-ceph-mon-endpoints
  namespace: rook-ceph
data:
  data: '%s'
  mapping: '%s'
  maxMonId: "12"
`

const dataThreeMons = "g=192.168.1.103:6789,j=192.168.1.107:6789,l=192.168.1.105:6789"

const mappingThreeMons = `{"node":{` +
	`"g":{"Name":"node-01","Hostname":"node-01","Address":"192.168.1.103"},` +
	`"j":{"Name":"node-03","Hostname":"node-03","Address":"192.168.1.107"},` +
	`"l":{"Name":"node-02","Hostname":"node-02","Address":"192.168.1.105"}}}`

// mappingFailover replaces mon g by mon m on another node.
const dataFailover = "j=192.168.1.107:6789,l=192.168.1.105:6789,m=192.168.1.109:3300"

const mappingFailover = `{"node":{` +
	`"j":{"Name":"node-03","Hostname":"node-03","Address":"192.168.1.107"},` +
	`"l":{"Name":"node-02","Hostname":"node-02","Address":"192.168.1.105"},` +
	`"m":{"Name":"node-04","Hostname":"node-04","Address":"192.168.1.109"}}}`

const cephMonDNSEndpoint = `apiVersion: rook-mon-dns.github.io/v1alpha1
kind: CephMonDNSEndpoint
metadata:
  name: rook-ceph
  namespace: rook-ceph
spec:
  zone: ceph.example.com
  srv:
    enabled: true
`

func kubectl(stdin string, args ...string) (string, error) {
	cmd := exec.Command("kubectl", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return utils.Run(cmd)
}

// dnsRecords returns the DNSEndpoint as "name type targets" lines.
func dnsRecords() (string, error) {
	return kubectl("", "get", "dnsendpoint", "rook-ceph", "-n", namespace, "-o",
		`jsonpath={range .spec.endpoints[*]}{.dnsName} {.recordType} {.targets}{"\n"}{end}`)
}

var _ = Describe("rook-mon-dns", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	BeforeAll(func() {
		By("creating the rook-ceph namespace")
		_, err := kubectl("", "create", "ns", namespace)
		Expect(err).NotTo(HaveOccurred())

		By("installing the ExternalDNS DNSEndpoint CRD")
		_, err = kubectl("", "apply", "-f", "test/crds/dnsendpoints.externaldns.k8s.io.yaml")
		Expect(err).NotTo(HaveOccurred())

		By("deploying the operator")
		_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("make", "undeploy", "ignore-not-found=true"))
		_, _ = kubectl("", "delete", "ns", namespace, "--ignore-not-found")
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{"logs", "-l", "control-plane=controller-manager", "-n", namespace, "--tail=100"},
			{"get", "events", "-n", namespace, "--sort-by=.lastTimestamp"},
			{"get", "cephmondnsendpoint", "-n", namespace, "-o", "yaml"},
			{"get", "dnsendpoint", "-n", namespace, "-o", "yaml"},
		} {
			out, _ := kubectl("", args...)
			_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s:\n%s\n", strings.Join(args, " "), out)
		}
	})

	It("runs the controller manager", func() {
		Eventually(func(g Gomega) {
			out, err := kubectl("", "get", "pods", "-l", "control-plane=controller-manager",
				"-n", namespace, "-o", "jsonpath={.items[*].status.phase}")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Running"))
		}).Should(Succeed())
	})

	It("publishes the mons as DNS records", func() {
		_, err := kubectl(fmt.Sprintf(monEndpointsConfigMap, dataThreeMons, mappingThreeMons), "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred())
		_, err = kubectl(cephMonDNSEndpoint, "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, err := kubectl("", "get", "cephmondnsendpoint", "rook-ceph", "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"))
		}).Should(Succeed())

		out, err := dnsRecords()
		Expect(err).NotTo(HaveOccurred())
		Expect(utils.GetNonEmptyLines(out)).To(ConsistOf(
			`mon1.ceph.example.com A ["192.168.1.103"]`,
			`mon2.ceph.example.com A ["192.168.1.107"]`,
			`mon3.ceph.example.com A ["192.168.1.105"]`,
			`mon-all.ceph.example.com A ["192.168.1.103","192.168.1.107","192.168.1.105"]`,
			`_ceph-mon._tcp.ceph.example.com SRV `+
				`["0 10 6789 mon1.ceph.example.com","0 10 6789 mon2.ceph.example.com","0 10 6789 mon3.ceph.example.com"]`,
		))
	})

	It("emits a Synced event", func() {
		Eventually(func(g Gomega) {
			out, err := kubectl("", "get", "events.events.k8s.io", "-n", namespace,
				"--field-selector", "regarding.kind=CephMonDNSEndpoint", "-o", "jsonpath={.items[*].reason}")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(ContainSubstring("Synced"))
		}).Should(Succeed())
	})

	It("keeps the records while the ConfigMap is briefly broken", func() {
		_, err := kubectl(fmt.Sprintf(monEndpointsConfigMap, "", "{}"), "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, err := kubectl("", "get", "cephmondnsendpoint", "rook-ceph", "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].reason}`)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("MappingEmpty"))
		}).Should(Succeed())

		out, err := dnsRecords()
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`mon1.ceph.example.com A ["192.168.1.103"]`))
	})

	It("follows a mon failover", func() {
		_, err := kubectl(fmt.Sprintf(monEndpointsConfigMap, dataFailover, mappingFailover), "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, err := dnsRecords()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(ContainSubstring(`mon3.ceph.example.com A ["192.168.1.109"]`))
			g.Expect(out).To(ContainSubstring(`"0 10 3300 mon3.ceph.example.com"`))
			g.Expect(out).NotTo(ContainSubstring("192.168.1.103"))
		}).Should(Succeed())
	})

	It("garbage-collects the DNSEndpoint when the CR is deleted", func() {
		_, err := kubectl("", "delete", "cephmondnsendpoint", "rook-ceph", "-n", namespace)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, err := kubectl("", "get", "dnsendpoint", "-n", namespace, "-o", "name")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(BeEmpty())
		}).Should(Succeed())
	})
})
