all: push

RELEASE?=v0.1.0
PREFIX?=docker.io/at28997146/k8s-ovs
DOCKER?=docker

build: clean
	go build -o rootfs/opt/cni/bin/k8s-ovs k8s-ovs/cniclient
	go build -o rootfs/usr/sbin/k8s-ovs  k8s-ovs

container: build
	cd rootfs && $(DOCKER) build --pull -t $(PREFIX):$(RELEASE) -f Dockerfile ./

push: container
	$(DOCKER) push $(PREFIX):$(RELEASE)

clean:
	rm -f rootfs/usr/sbin/k8s-ovs
	rm -f rootfs/opt/cni/bin/k8s-ovs
