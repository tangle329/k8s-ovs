# k8s-ovs
==============================

最近在寻求一些工作机会，如果有kubernetes相关研发招聘的朋友，欢迎随时联系我。我的个人简历可以通过百度网盘：https://pan.baidu.com/s/1jI20TWa 下载。谢谢

k8s-ovs是一个使用[openvswitch](http://openvswitch.org/)为[K8S](https://kubernetes.io/)提供SDN功能的项目。该项目基于[openshift SDN](https://docs.openshift.org/latest/architecture/additional_concepts/sdn.html)的原理进行开发。由于[openshift](https://github.com/openshift/origin)的SDN网络方案和openshift自身的代码耦合在一起，无法像[flannel](https://github.com/coreos/flannel)和[calico](https://github.com/projectcalico/calico)等网络方案以插件的方式独立的为K8S提供服务，所以我开发了k8s-ovs，它拥有openshift优秀的SDN功能，又可以独立为K8S提供服务。

该项目中有一部分基础代码库是从openshift的pkg/sdn/plugin直接拷贝或进行了一些修改的。如果有License方面的问题请随时联系我进行修正：at28997146@163.com。

如果对该项目有任何疑问，欢迎加入k8s-ovs-sdn的QQ交流群`477023854`进行讨论。

下面将对k8s-ovs的功能和安装进行详细介绍。如果你想了解不同功能的配置方法，可以跳转到[admin.md](https://github.com/tangle329/k8s-ovs/blob/master/admin.md)进行阅读。

## k8s-ovs的功能
---------------

k8s-ovs支持单租户模式和多租户模式。

* 单租户模式直接使用openvswitch+vxlan将K8S的POD网络组成一个大二层，所有[POD](https://kubernetes.io/docs/concepts/workloads/pods/pod-overview/)可以互通。
* 多租户模式也使用openvswitch+vxlan来组建K8S的POD网络，但是它可以基于K8S中的[NAMESPACE](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/)来分配虚拟网络从而形成一个网络独立的租户，一个NAMESPACE中的POD无法访问其他NAMESPACE中的PODS和[SERVICES](https://kubernetes.io/docs/concepts/services-networking/service/)。
* 多租户模式下可以对一些NAMESPACE进行设置，使这些NAMESPACE中的POD可以和其他所有NAMESPACE中的PODS和SERVICES进行互访。
* 多租户模式下可以合并某两个NAMESPACE的虚拟网络，让他们的PODS和SERVICES可以互访。
* 多租户模式下也可以将上面合并的NAMESPACE虚拟网络进行分离。
* 单租户和多租户模式下都支持POD的流量限制功能，这样可以保证同一台主机上的POD相对公平的分享网卡带宽，而不会出现一个POD因为流量过大占满了网卡导致其他POD无法正常工作的情况。
* 单租户和多租户模式下都支持外联负载均衡。

## 安装
---------------

安装部署，需要准备至少3台服务器，其中一台作为K8S的master，另外两台作为node节点。我的测试环境为Centos7.2，docker(1.12.6)版本以及golang(1.7.1)版本。每台node节点都需要安装[openvswitch-2.5.0或以上版本](https://github.com/openvswitch/ovs/archive/v2.5.2.tar.gz)，并且每台node节点都需要将`ovsdb-server`和`ovs-vswitchd`运行起来。

### K8S集群安装

请参考[K8S安装手册](https://kubernetes.io/docs/setup/pick-right-solution/)，推荐安装v1.6.0以后的版本，因为之前版本的kubelet在使用CNI的情况下存在[IP地址泄漏问题](https://github.com/kubernetes/kubernetes/pull/37036)。

1，K8S集群安装过程中应该跳过网络部署这一步，网络部署将由下面的k8s-ovs部署完成。

2，安装过程中需要设置[kubelet](https://kubernetes.io/docs/concepts/overview/components/#kubelet)使用[cni](https://github.com/containernetworking/cni)，也就是kubelet启动参数需要设置为`--network-plugin=cni --cni-conf-dir=/etc/cni/net.d --cni-bin-dir=/opt/cni/bin`，如果kubelet是使用容器的方式启动的需要将`/etc/cni/net.d`，`/opt/cni/bin`和`/var/run/`挂在到kubelet内部。

3，安装完成后K8S的node节点将会呈现出下面的状态。`NotReady`是因为还没有部署网络，kubelet在`/etc/cni/net.d/`目录下面没有发现cni配置文件导致，这会随着后面网络的部署会得到解决。

```
$ kubectl get node
NAME        STATUS     AGE       VERSION
sdn-test1   NotReady   10s       v1.6.4
sdn-test2   NotReady   4m        v1.6.4
sdn-test3   NotReady   6s        v1.6.4
```

### 安装k8s-ovs
下面我们将会分两种情况进行安装，用户可以选择其中适合自己的一种。
1，使用yaml直接一键部署k8s-ovs到k8s集群中，并使其作为daemonset运行起来。
2，详细介绍k8s-ovs的每一个组件的安装步骤，以便用户对k8s-ovs的各个组件依赖关系有一个深入了解。

开始下列安装操作的前提是你已经按照上面步骤安装好了K8S集群。并且在每一台node节点上将`ovsdb-server`和`ovs-vswitchd`运行起来了。

#### 快速安装
快速安装需要你部署K8S 1.6以上版本的集群，如果是1.5或者1.4的集群请下载yaml文件做相应修改。

```
$ kubectl apply -f https://raw.githubusercontent.com/tangle329/k8s-ovs/master/rootfs/k8s-ovs.yaml
```

上面命令成功返回之后，你可以通过下列查询命令获取pod和node的运行状态来确认是否安装成功：

```
$ kubectl get pod --namespace=kube-system | grep k8s-ovs
k8s-ovs-etcd-h0fsc                                   1/1       Running   0          2h
k8s-ovs-node-c27jr                                   1/1       Running   0          2h
k8s-ovs-node-fxwwl                                   1/1       Running   0          2h
k8s-ovs-node-p09jd                                   1/1       Running   0          2h
$ kubectl get node
NAME        STATUS    AGE       VERSION
sdn-test1   Ready     11m       v1.6.4
sdn-test2   Ready     15m       v1.6.4
sdn-test3   Ready     11m       v1.6.4
```

至此，k8s-ovs部署完成，用户可以跳转到[admin.md](https://github.com/tangle329/k8s-ovs/blob/master/admin.md)进行功能配置了。

#### 详细安装

详细安装需要你部署K8S v1.4版本以上的集群。
下列命令需要到每台K8S的node节点运行，也可以在一台服务器上将对应文件编译好，然后使用批量部署工具将对应文件谁送到所有node节点上。
你也可以使用[k8s-ovs-rpm](https://github.com/tangle329/k8s-ovs-rpm)项目中的RPM SPEC来制作本项目的RPM包，然后直接安装RPM包来完成下列命令的操作。

```
$ cd $GOPATH/src/
$ git clone https://github.com/tangle329/k8s-ovs.git
$ cd k8s-ovs
$ go build -o rootfs/opt/cni/bin/k8s-ovs k8s-ovs/cniclient
$ cp rootfs/opt/cni/bin/k8s-ovs /opt/cni/bin/
$ cp rootfs/opt/cni/bin/host-local /opt/cni/bin/
$ cp rootfs/opt/cni/bin/loopback /opt/cni/bin/
$ cp rootfs/etc/cni/net.d/80-k8s-ovs.conf /etc/cni/net.d/
$ go build -o rootfs/usr/sbin/k8s-ovs  k8s-ovs
$ cp rootfs/usr/sbin/k8s-ovs /usr/sbin/
$ cp rootfs/usr/sbin/k8s-sdn-ovs /usr/sbin/
```

其中第一个`go build -o rootfs/opt/cni/bin/k8s-ovs k8s-ovs/cniclient`生成的k8s-ovs是cni客户端，kubelet在创建和删除POD的时候会调用它来对POD的网络部分进行配置。第二个`go build -o rootfs/usr/sbin/k8s-ovs  k8s-ovs`生成的k8s-ovs是我们的整个k8s-ovs的核心，前面提到的所有功能都由它来实现，它也是cni的服务端，接受并处理前面cni客户端的请求。注意请不要把/opt/cni/bin/目录设置到PATH环境变量中。

通常在kubelet使用了cni的情况下要执行了`cp rootfs/etc/cni/net.d/80-k8s-ovs.conf /etc/cni/net.d/`命令之后k8s的node节点才会进行ready状态，另外请确保在/etc/cni/net.d/中只有80-k8s-ovs.conf这个文件，执行完上面的命令后K8S的node节点状态为：

```
$ kubectl get node
NAME        STATUS    AGE       VERSION
sdn-test1   Ready     11m       v1.6.4
sdn-test2   Ready     15m       v1.6.4
sdn-test3   Ready     11m       v1.6.4
```

### 设置k8s-ovs的网络参数

设置网络参数之前，你需要搭建一个etcd服务，或者和K8S的[apiserver](https://kubernetes.io/docs/concepts/overview/components/#kube-apiserver)共用一个etcd服务，所有K8S节点都需要能访问到该etcd服务。

搭建好etcd服务器之后，使用下列命令设置k8s-ovs的网络参数：

```
$ etcdctl set /k8s.ovs.com/ovs/network/config '{"Name":"k8ssdn", "Network":"172.11.0.0/16", "HostSubnetLength":10, "ServiceNetwork":"10.96.0.0/12", "PluginName":"k8s-ovs-multitenant"}'
```

其中，`Network`用于设置整个K8S集群POD网络的网段；`HostSubnetLength`用于设置每个node节点的子网长度；`ServiceNetwork`用于设置K8S中service的网段，这个需要和K8S apiserver的参数`--service-cluster-ip-range`指定的网络保持一致；`PluginName`用于设置租户模式，`k8s-ovs-multitenant`用于设置多租户模式，`k8s-ovs-subnet`用于设置单租户模式。

### 启动k8s-ovs

1，启动之前要在每个K8S node节点上设置访问K8S apiserver的环境变量，k8s-ovs就是通过该环境变量和apiserver进行通信的。
如果K8S使用的非加密方式则需要设置KUBERNETES_MASTER，你需要把下面两个变量`apiserver_vip`和`apiserver_port`替换成你自己的apiserver服务的ip和port：

```
$ export KUBERNETES_MASTER="${apiserver_vip}:${apiserver_port}"
```

如果K8S使用加密方式则需要设置KUBECONFIG环境变量。我们使用的是加密方式所以设置的KUBECONFIG环境变量，其中每一台节点上面都需要有/etc/kubernetes/admin.conf这个文件，该文件是在部署加密方式服务的K8S集群时在K8S master上生成的，你需要将它依次拷贝到每一台node节点上：

```
$ export KUBECONFIG="/etc/kubernetes/admin.conf"
```

2，设置好环境变量后就可以运行k8s-ovs了。k8s-ovs有几个重要的选项`--etcd-endpoints`用于指定etcd服务的访问ip+port列表；如果是加密的etcd服务可以通过`--etcd-cafile`，`--etcd-certfile`和`--etcd-keyfile`来指定CA，证书，秘钥；`--etcd-prefix`用于指定k8s-ovs网络配置存放的目录，需要和前面网络配置小节中`etcdctl set`命令指定的目录一样；`--hostname`用于指定k8s-ovs所运行的node节点的名字，该名字需要和前面`kubectl get node`输出的名字一致，通常`--hostname`不需要指定，但有时候一些K8S集群的部署脚本会通过给kubelet传递`--hostname-override`选项来覆盖默认node节点名，这时就需要设置k8s-ovs的`--hostname`以便能够保持一致。
由于我们的环境没有覆盖node节点名，etcd也没有使用加密方式，所以运行命令如下：

```
$ /usr/sbin/k8s-ovs --etcd-endpoints=http://${etcd_ip}:2379 --etcd-prefix=/k8s.ovs.com/ovs/network --alsologtostderr --v=5
```

至此，k8s-ovs部署完成，用户可以跳转到[admin.md](https://github.com/tangle329/k8s-ovs/blob/master/admin.md)进行功能配置了。
