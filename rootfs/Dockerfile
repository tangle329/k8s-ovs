# Pull base image
FROM docker.io/centos:7.2.1511

MAINTAINER Tang Le "at28997146@163.com"

ADD https://github.com/tangle329/k8s-ovs/releases/download/v0.1.0/openvswitch-2.5.2-1.x86_64.rpm /

ADD usr/sbin/k8s-ovs              /usr/sbin/
ADD usr/sbin/k8s-sdn-ovs          /usr/sbin/
ADD opt/cni/bin/k8s-ovs           /opt/cni/bin/
ADD opt/cni/bin/host-local        /opt/cni/bin/
ADD opt/cni/bin/loopback          /opt/cni/bin/
ADD etc/cni/net.d/80-k8s-ovs.conf /etc/cni/net.d/
ADD k8s-ovs-wrapper               /
ADD usr/sbin/etcdctl              /usr/sbin/

RUN yum install -y iptables
RUN yum install -y iproute
RUN yum install -y logrotate
RUN rpm -ivh /openvswitch-2.5.2-1.x86_64.rpm

CMD ["/k8s-ovs-wrapper"]
