# k8s-ovs
==============================

k8s-ovs是一个使用[openvswitch](http://openvswitch.org/)为[K8S](https://kubernetes.io/)提供SDN功能的项目。该项目基于[openshift SDN](https://docs.openshift.org/latest/architecture/additional_concepts/sdn.html)的原理进行开发。由于[openshift](https://github.com/openshift/origin)的SDN网络方案和openshift自身的代码耦合在一起，无法像[flannel](https://github.com/coreos/flannel)和[calico](https://github.com/projectcalico/calico)等网络方案以插件的方式独立的为K8S提供服务，所以我（隶属于万达网络科技集团云平台部）开发了k8s-ovs，它拥有openshift优秀的SDN功能，又可以独立为K8S提供服务。

该项目中有一部分基础代码库是从openshift的pkg/sdn/plugin直接拷贝或进行了一些修改的。如果有License方面的问题请随时联系我进行修正：tangle3@wanda.cn。

如果对该项目有任何疑问，欢迎加入k8s-ovs-sdn的QQ交流群`477023854`进行讨论。

下面我们将从k8s-ovs的功能，安装和功能配置三个方面对k8s-ovs进行详细介绍。

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

安装部署，需要准备至少3台服务器，其中一台作为K8S的master，另外两台作为node节点。我的测试环境为Centos7.2，docker(1.12.6)版本以及golang(1.7.1)版本。每台节点都需要安装[openvswitch-2.5.0或以上版本](https://github.com/openvswitch/ovs/archive/v2.5.2.tar.gz)。整个k8s-ovs的安装过程可以通过直接制作镜像和编写K8S的[DaemonSet](https://kubernetes.io/docs/concepts/workloads/controllers/daemonset/)来进行，但是为了说明k8s-ovs和各个组件的依赖关系，这里将一步一步手动进行安装。

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

### k8s-ovs相关文件部署

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

至此，整个集群部署完成，你可以开始运用k8s-ovs的功能来为业务服务了。


## 功能配置
---------------

由于多租户模式的功能包含了单租户模式的功能，所以这里只介绍多租户模式。下列测试中的image可以使用`docker pull docker.io/at28997146/nginx-hello:v2.0`获取。

### 多租户功能与测试
按照前面安装小节部署好之后，多租户功能就自动使能了，所以不需要额外配置。
测试方式：创建两个命名空间`helloworld1`和`helloworld2`，在`helloworld1`中创建两个POD和与之关联的一个SERVICE，在`helloworld2`中创建两个POD和与之关联的一个SERVICE。单个命名空间内的POD之间以及POD与SERVICE之间应该可以互通，不同命名空间的POD之间以及POD和SERVICE之间应该不可以互通。实例如下：

```
$ kubectl get pod --namespace=helloworld1 -o wide                                <== 获取helloworld1中的POD
NAME                           READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld1-2922187381-dz274   1/1       Running   0          20m       172.11.8.140   sdn-test2
helloworld1-2922187381-nwn40   1/1       Running   0          20m       172.11.4.19    sdn-test3

$ kubectl get svc --namespace=helloworld1                                       <== 获取helloworld1中的SERVICE
NAME          CLUSTER-IP      EXTERNAL-IP   PORT(S)   AGE
helloworld1   10.101.242.89   <none>        80/TCP    20m

$ kubectl get pod --namespace=helloworld2 -o wide                               <== 获取helloworld2中的POD
NAME                           READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld2-3233221239-6pj3c   1/1       Running   0          2m        172.11.8.144   sdn-test2
helloworld2-3233221239-wl2mv   1/1       Running   0          2m        172.11.4.24    sdn-test3

$ kubectl get svc --namespace=helloworld2                                       <== 获取helloworld2中的SERVICE
NAME          CLUSTER-IP      EXTERNAL-IP   PORT(S)   AGE
helloworld2   10.98.243.167   <none>        80/TCP    2m

$ kubectl exec -it helloworld1-2922187381-dz274 /bin/sh --namespace=helloworld1 <== 进入helloworld1中的POD

sh-4.1# ping 172.11.4.19                                                        <== 从helloworld1中的POD去ping helloworld1的另一个POD能通
PING 172.11.4.19 (172.11.4.19) 56(84) bytes of data.
64 bytes from 172.11.4.19: icmp_seq=1 ttl=64 time=1.55 ms
64 bytes from 172.11.4.19: icmp_seq=2 ttl=64 time=0.191 ms

sh-4.1# curl 10.101.242.89                                                      <== 从helloworld1中的POD去访问helloworld1中的SERVICE也能通
Hello nginx

sh-4.1# ping 172.11.8.144                                                       <== 从helloworld1中的POD去访问helloworld2中的POD不能通
PING 172.11.8.144 (172.11.8.144) 56(84) bytes of data.
^C
--- 172.11.8.144 ping statistics ---
6 packets transmitted, 0 received, 100% packet loss, time 5093ms

sh-4.1# curl 10.98.243.167                                                      <== 从helloworld1中的POD去访问helloworld2中的SERVICE也不能通
^C
```

### 租户网络合并，分离和全网化功能。

合并是指两个不同租户的网络变成一个虚拟网络从而使这两个租户中的所有POD和SERVICE能够互通；

分离是指针对合并的两个租户，如果用户希望这两个租户不再互通了则可以将他们进行分离；

全网化是指有一些特殊的服务需要能够和其他所有的租户互通，那么通过将这种特殊的租户进行全网化操作就可以实现。

不同租户的网络隔离是通过为每个K8S命名空间分配一个VNI(VXLAN中的概念)来实现的，在VXLAN中不同的VNI可以隔离不同的网络空间。k8s-ovs将具体的K8S命名空间和VNI的对应关系存储在etcd中，如下：

```
$ etcdctl ls /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces
/k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld1
/k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld2

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld1
{"NetName":"helloworld1","NetID":300924,"Action":"","Namespace":""}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld2
{"NetName":"helloworld2","NetID":3831805,"Action":"","Namespace":""}
```

这是在我们通过K8S创建NAMESPACE时，k8s-ovs自动检测并为我们创建的。其中`NetName`是指租户的K8S命名空间；`NetID`是指为该租户分配的VNI；`Action`是指可以对该租户网络进行的操作，它包括`join`:合并, `isolate`:分离, `global`:全网化，其中`join`需要指定上面的第四个参数Namespace，用于表示需要和哪个租户进行合并，其他两个操作则不需要设置Namespace。

1，这里我们将上面创建的两个租户helloworld1和helloworld2进行合并，合并操作如下：

```
$ etcdctl update /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld1 '{"NetName":"helloworld1","NetID":300924,"Action":"join","Namespace":"helloworld2"}'
{"NetName":"helloworld1","NetID":300924,"Action":"join","Namespace":"helloworld2"}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld1
{"NetName":"helloworld1","NetID":3831805,"Action":"","Namespace":""}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld2
{"NetName":"helloworld2","NetID":3831805,"Action":"","Namespace":""}
```

合并之后观察helloworld1和helloworld2，发现两个租户的`NetID`变为相同的了。这样两个网络的POD和SERVICE就可以相互访问了。测试如下：

```
$ kubectl get pod --namespace=helloworld1 -o wide                                 <== 获取helloworld1中的POD
NAME                           READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld1-2922187381-c0fml   1/1       Running   0          3m        172.11.4.29    sdn-test2
helloworld1-2922187381-dwvdk   1/1       Running   0          3m        172.11.8.150   sdn-test3

$ kubectl get svc --namespace=helloworld1                                         <== 获取helloworld1中的SERVICE
NAME          CLUSTER-IP       EXTERNAL-IP   PORT(S)   AGE
helloworld1   10.100.225.143   <none>        80/TCP    3m

$ kubectl get pod --namespace=helloworld2 -o wide                                 <== 获取helloworld2中的POD
NAME                           READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld2-3233221239-1ks8n   1/1       Running   0          5m        172.11.4.30    sdn-test2
helloworld2-3233221239-g1d2w   1/1       Running   0          5m        172.11.8.151   sdn-test3

$ kubectl get svc --namespace=helloworld2                                         <== 获取helloworld2中的SERVICE
NAME          CLUSTER-IP     EXTERNAL-IP   PORT(S)   AGE
helloworld2   10.108.57.44   <none>        80/TCP    6m

$ kubectl exec -it helloworld1-2922187381-c0fml /bin/sh --namespace=helloworld1   <== 进入helloworld1中的pod
sh-4.1# ping 172.11.4.30                                                          <== 从helloworld1中的POD去访问helloworld2中的POD能通
PING 172.11.4.30 (172.11.4.30) 56(84) bytes of data.
64 bytes from 172.11.4.30: icmp_seq=1 ttl=64 time=1.29 ms
64 bytes from 172.11.4.30: icmp_seq=2 ttl=64 time=0.044 ms
^C
--- 172.11.4.30 ping statistics ---
2 packets transmitted, 2 received, 0% packet loss, time 1352ms
rtt min/avg/max/mdev = 0.044/0.669/1.294/0.625 ms

sh-4.1# curl 10.108.57.44                                                         <== 从helloworld1中的POD去访问helloworld2中的SERVICE能通
Hello nginx
```

2，下面我们将上面合并的两个命名空间进行分离，操作如下：

```
$ etcdctl update /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld1 '{"NetName":"helloworld1","NetID":3831805,"Action":"isolate","Namespace":""}'
{"NetName":"helloworld1","NetID":3831805,"Action":"isolate","Namespace":""}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld1
{"NetName":"helloworld1","NetID":6693608,"Action":"","Namespace":""}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld2
{"NetName":"helloworld2","NetID":3831805,"Action":"","Namespace":""}
```

分离之后观察helloworld1和helloworld2，发现两个租户的NetID由合并之后的相同变成了现在的不同，这样两个网络中的POD和SERVICE就再次变成了不可访问。测试如下：

```
$ kubectl exec -it helloworld1-2922187381-c0fml /bin/sh --namespace=helloworld1  <== 进入helloworld1中的POD
sh-4.1# ping 172.11.4.30                                                         <== 从helloworld1中的POD去访问helloworld2中的POD不能通
PING 172.11.4.30 (172.11.4.30) 56(84) bytes of data.
^C
--- 172.11.4.30 ping statistics ---
3 packets transmitted, 0 received, 100% packet loss, time 2568ms

sh-4.1# curl 10.108.57.44                                                        <== 从helloworld1中的POD去访问helloworld2中的SERVICE不能通
^C
```

3，下面我们再创建一个租户helloworld3，然后我们将helloworld3进行全网化操作，这样我们的集群中将存在三个租户，并且helloworld1和helloworld2之间无法通行，但是helloworld3将能够和另外两个租户通行操作如下：

```
$ kubectl create namespace helloworld3
namespace "helloworld3" created

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld3
{"NetName":"helloworld3","NetID":12800140,"Action":"","Namespace":""}

$ etcdctl update /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld3 '{"NetName":"helloworld3","NetID":12800140,"Action":"global","Namespace":""}'  <== 全网化操作
{"NetName":"helloworld3","NetID":12800140,"Action":"global","Namespace":""}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld1
{"NetName":"helloworld1","NetID":6693608,"Action":"","Namespace":""}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld2
{"NetName":"helloworld2","NetID":3831805,"Action":"","Namespace":""}

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/netnamespaces/helloworld3       <== 对helloworld3进行了全网通之后NetID变为了0
{"NetName":"helloworld3","NetID":0,"Action":"","Namespace":""}
```

全网化之后我们观察到helloworld3的NetID变为了0，下面我们来进行通信测试：

```
$ kubectl get pod --namespace=helloworld1 -o wide                              <== 获取helloworld1中的POD
NAME                           READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld1-2922187381-c0fml   1/1       Running   0          30m       172.11.4.29    sdn-test2
helloworld1-2922187381-dwvdk   1/1       Running   0          30m       172.11.8.150   sdn-test3

$kubectl get svc --namespace=helloworld1                                       <== 获取helloworld1中的SERVICE
NAME          CLUSTER-IP       EXTERNAL-IP   PORT(S)   AGE
helloworld1   10.100.225.143   <none>        80/TCP    31m

$ kubectl get pod --namespace=helloworld2 -o wide                              <== 获取helloworld2中的POD
NAME                           READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld2-3233221239-1ks8n   1/1       Running   0          30m       172.11.4.30    sdn-test2
helloworld2-3233221239-g1d2w   1/1       Running   0          30m       172.11.8.151   sdn-test3

$ kubectl get svc --namespace=helloworld2                                      <== 获取helloworld2中的SERVICE
NAME          CLUSTER-IP     EXTERNAL-IP   PORT(S)   AGE
helloworld2   10.108.57.44   <none>        80/TCP    31m

$ kubectl get pod --namespace=helloworld3 -o wide                              <== 获取helloworld3中的POD
NAME                           READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld3-3544255097-2lg46   1/1       Running   0          27s       172.11.4.31    sdn-test2
helloworld3-3544255097-52dz1   1/1       Running   0          27s       172.11.8.152   sdn-test3

$ kubectl get svc --namespace=helloworld3                                      <== 获取helloworld3中的SERVICE
NAME          CLUSTER-IP     EXTERNAL-IP   PORT(S)   AGE
helloworld3   10.98.68.157   <none>        80/TCP    2m

$ kubectl exec -it helloworld3-3544255097-2lg46 /bin/sh --namespace=helloworld3 <== 进入helloworld3的POD中
sh-4.1# ping 172.11.4.29                                                        <== 从helloworld3中的POD去访问helloworld1中的POD能通
PING 172.11.4.29 (172.11.4.29) 56(84) bytes of data.
64 bytes from 172.11.4.29: icmp_seq=1 ttl=64 time=0.584 ms
^C
--- 172.11.4.29 ping statistics ---
1 packets transmitted, 1 received, 0% packet loss, time 529ms
rtt min/avg/max/mdev = 0.584/0.584/0.584/0.000 ms

sh-4.1# curl 10.100.225.143                                                     <== 从helloworld3中的POD去访问helloworld1中的SERVICE能通
Hello nginx

sh-4.1# ping 172.11.4.30                                                        <== 从helloworld3中的POD去访问helloworld2中的POD能通
PING 172.11.4.30 (172.11.4.30) 56(84) bytes of data.
64 bytes from 172.11.4.30: icmp_seq=1 ttl=64 time=0.544 ms
^C
--- 172.11.4.30 ping statistics ---
1 packets transmitted, 1 received, 0% packet loss, time 469ms
rtt min/avg/max/mdev = 0.544/0.544/0.544/0.000 ms

sh-4.1# curl 10.108.57.44                                                       <== 从helloworld3中的POD去访问helloworld2中的SERVICE能通
Hello nginx
```

### 流量限制功能

流量限制功能通过在POD的`annotations`字段设置`kubernetes.io/ingress-bandwidth`(设置输入流量带宽)和`kubernetes.io/egress-bandwidth`(设置输出流量带宽)来实现。 例如下面的yaml文件，设置了POD的出口为10M，入口带宽为10M：
```
apiVersion: apps/v1beta1 # for versions before 1.6.0 use extensions/v1beta1
kind: Deployment
metadata:
  name: helloworld1
  namespace: helloworld1
spec:
  replicas: 1
  template:
    metadata:
      labels:
        run: helloworld1
      annotations:
        kubernetes.io/ingress-bandwidth: 10M
        kubernetes.io/egress-bandwidth: 10M
    spec:
      containers:
      - name: helloworld1
        image: docker.io/at28997146/nginx-hello:v2.0
        ports:
        - containerPort: 80
        resources:
          limits:
            cpu: "100m"
            memory: 1Gi
          requests:
            cpu: "100m"
            memory: 1Gi
```

通过上面的yaml文件我们来创建POD，并对POD进行测试，下面将在helloworld1中创建两个POD，其中一个设置了入口和出口流量限制，另外一个完全没有设置。然后我们将在其中一个POD中启动iperf的服务，另外一个POD中启动iperf客户端来进行带宽测试。具体操作如下：

```
$ kubectl get pod --namespace=helloworld1 -o wide                                                        <== 从helloworld1中获取POD
NAME                             READY     STATUS    RESTARTS   AGE       IP             NODE
helloworld1-1-2154498866-m64d4   1/1       Running   0          13s       172.11.4.41    sdn-test2       <== 没有设置流量限制的POD
helloworld1-3097699521-sx6ll          1/1       Running   0          1m        172.11.8.162  sdn-test3   <== 设置了流量限制的POD

$ kubectl exec -it helloworld1-1-2154498866-m64d4 /bin/sh --namespace=helloworld1                        <== 进入没有设置流量限制的POD
sh-4.1# iperf3 -s                                                                                                                              <== 启动iperf服务器

$ kubectl exec -it helloworld1-3097699521-sx6ll /bin/sh --namespace=helloworld1                          <== 进入设置了流量限制的POD
sh-4.1# iperf3 -c 172.11.4.41 -t 10                                                                      <== 从设置了流量限制的POD启动客服端
Connecting to host 172.11.4.41, port 5201
[  4] local 172.11.8.162 port 53838 connected to 172.11.4.41 port 5201
[ ID] Interval           Transfer     Bandwidth       Retr  Cwnd
[  4]   0.00-1.00   sec  2.21 MBytes  18.6 Mbits/sec  215   51.9 KBytes
[  4]   1.00-2.00   sec  1.23 MBytes  10.3 Mbits/sec  157   28.7 KBytes
[  4]   2.00-3.00   sec  1.17 MBytes  9.78 Mbits/sec  140   16.4 KBytes
[  4]   3.00-4.00   sec  1.41 MBytes  11.8 Mbits/sec  121   34.1 KBytes
[  4]   4.00-5.00   sec  1.10 MBytes  9.26 Mbits/sec  131   15.0 KBytes
[  4]   5.00-6.00   sec  1.17 MBytes  9.78 Mbits/sec  103   30.0 KBytes
[  4]   6.00-7.00   sec  1.29 MBytes  10.8 Mbits/sec  145   15.0 KBytes
[  4]   7.00-8.00   sec   879 KBytes  7.20 Mbits/sec   92   15.0 KBytes
[  4]   8.00-9.00   sec  1.41 MBytes  11.8 Mbits/sec  149   27.3 KBytes
[  4]   9.00-10.00  sec  1.04 MBytes  8.74 Mbits/sec  114   1.37 KBytes
- - - - - - - - - - - - - - - - - - - - - - - - -
[ ID] Interval           Transfer     Bandwidth       Retr
[  4]   0.00-10.00  sec  12.9 MBytes  10.8 Mbits/sec  1367             sender
[  4]   0.00-10.00  sec  12.5 MBytes  10.5 Mbits/sec                  receiver

iperf Done.
```

从上面的测试结果可以看到，带宽被限制在了大约10M。

### 外联负载均衡

对于K8S集群，官方提供了kube-proxy来支持负载均衡，同时社区也提供了一些运行在K8S集群中的daemon，如：[ingress controller](https://github.com/kubernetes/ingress)来支持负载均衡。但是对于很多公司，已经有现成实现好的，并且性能非常高的统一负载均衡服务。这种情况下我们需要一种方法来让这些独立于K8S集群的负载均衡服务器和K8S的POD网络打通。对此k8s-ovs提供了配置外联负载均衡的方式。介绍如下：

k8s-ovs在为K8S组网的时候会为每一台node节点分配一个网段，并记录在etcd中，同时k8s-ovs会配置每台节点的openvswitch来实现这些网段之间的互通。对于不属于K8S集群的服务器，由于它不是K8S的一个节点，所以我们需要在这些服务器上运行k8s-ovs(按照上面安装小节的步骤来运行)，然后通过一种方式来为这些服务器分配网段，最后运行的k8s-ovs就可以针对这个网段对openvswitch进行配置，从而实现服务器和整个K8S集群的网络通行。

我们测试的三台集群的网段分配在etcd中的表现如下：

```
$ etcdctl ls /k8s.ovs.com/ovs/network/k8ssdn/subnets
/k8s.ovs.com/ovs/network/k8ssdn/subnets/x.x.x.197
/k8s.ovs.com/ovs/network/k8ssdn/subnets/x.x.x.196
/k8s.ovs.com/ovs/network/k8ssdn/subnets/x.x.x.198

$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/subnets/x.x.x.197
{"Host":"sdn-test1","HostIP":"x.x.x.197","Subnet":"172.11.0.0/22","Assign":false}
```

上例中`x.x.x`是node节点ip地址的前三个数，这里由于隐私原因进行了隐藏。上面的etcd输出是k8s-ovs为K8S节点自动生成的。对于非K8S集群的node节点，如果要分配网段，则只需要按下列命令进行操作即可，注意：该操作中`Subnet`字段为空，`Assign`字段设置为true（true意味着请求k8s-ovs为该服务器分配子网），`Host`和`HostIP`则设置为负载均衡服务器的主机名和ip地址：

```
$ etcdctl set /k8s.ovs.com/ovs/network/k8ssdn/subnets/x.x.x.199 '{"Host":"sdn-test4","HostIP":"x.x.x.197","Subnet":"","Assign":true}'
{"Host":"sdn-test4","HostIP":"x.x.x.197","Subnet":"","Assign":true}
$ etcdctl get /k8s.ovs.com/ovs/network/k8ssdn/subnets/x.x.x.199
{"Host":"sdn-test4","HostIP":"x.x.x.197","Subnet":"172.11.12.0/22","Assign":false}
```

如上，设置之后，再重新获取该主机的网段信息，发现k8s-ovs分配了一个网段`172.11.12.0/22`出来。
现在我们在负载均衡服务器上ping K8S集群中的所有POD，来验证连通性：

```
$ kubectl get pod --all-namespaces -o wide                      <== 获取集群中所有命名空间的POD
NAMESPACE     NAME                                                 READY     STATUS    RESTARTS   AGE       IP               NODE
helloworld1   helloworld1-1-2154498866-p66kb                       1/1       Running   0          21s       172.11.8.163     sdn-test2
helloworld2   helloworld2-3376745080-9w8xd                         1/1       Running   0          19s       172.11.4.42      sdn-test2
helloworld2   helloworld2-3376745080-pqswq                         1/1       Running   0          19s       172.11.8.164     sdn-test3
helloworld3   helloworld3-3728942277-smtgg                         1/1       Running   0          16s       172.11.4.43      sdn-test2
helloworld3   helloworld3-3728942277-xjv0z                         1/1       Running   0          16s       172.11.8.165     sdn-test3

$ ping 172.11.8.163                                              <== 从负载均衡集群ping helloworld1中的POD，能通
PING 172.11.8.163 (172.11.8.163) 56(84) bytes of data.
64 bytes from 172.11.8.163: icmp_seq=1 ttl=64 time=2.21 ms
^C
--- 172.11.8.163 ping statistics ---
1 packets transmitted, 1 received, 0% packet loss, time 0ms
rtt min/avg/max/mdev = 2.215/2.215/2.215/0.000 ms


$ ping 172.11.4.42                                               <== 从负载均衡集群ping helloworld2中的POD，能通
PING 172.11.4.42 (172.11.4.42) 56(84) bytes of data.
64 bytes from 172.11.4.42: icmp_seq=1 ttl=64 time=1.42 ms
^C
--- 172.11.4.42 ping statistics ---
1 packets transmitted, 1 received, 0% packet loss, time 0ms
rtt min/avg/max/mdev = 1.425/1.425/1.425/0.000 ms


$ ping 172.11.4.43                                                <== 从负载均衡集群ping helloworld3中的POD，能通
PING 172.11.4.43 (172.11.4.43) 56(84) bytes of data.
64 bytes from 172.11.4.43: icmp_seq=1 ttl=64 time=1.48 ms
^C
--- 172.11.4.43 ping statistics ---
1 packets transmitted, 1 received, 0% packet loss, time 0ms
rtt min/avg/max/mdev = 1.489/1.489/1.489/0.000 ms
```

从上面验证可以看出，负载均衡服务器可以和K8S集群中所有POD进行通信，接下来就可以直接部署nginx或者LVS来进行负载均衡部署了。
