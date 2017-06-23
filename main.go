package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/daemon"
	"github.com/golang/glog"
	"github.com/spf13/pflag"
	"golang.org/x/net/context"

	client "k8s.io/kubernetes/pkg/client/unversioned"

	kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubelet/dockertools"
	utilwait "k8s.io/kubernetes/pkg/util/wait"

	"k8s-ovs/pkg/election"
	"k8s-ovs/pkg/etcdmanager/etcdv2"
	"k8s-ovs/pkg/utils"
	"k8s-ovs/ksdn"
)

type CmdLineOpts struct {
	etcdEndpoints *string
	etcdPrefix    *string
	etcdKeyfile   *string
	etcdCertfile  *string
	etcdCAFile    *string
	etcdUsername  *string
	etcdPassword  *string
	network       *string
	hostname      *string
	dEndpoint     *string
	version       *bool
}

var (
	opts    CmdLineOpts
	version string = "0.1.0"
	leader  string
	flags   = pflag.NewFlagSet("", pflag.ExitOnError)
)

func init() {
	opts.etcdEndpoints = flags.String("etcd-endpoints", "http://127.0.0.1:4001,http://127.0.0.1:2379", "a comma-delimited list of etcd endpoints")
	opts.etcdPrefix = flags.String("etcd-prefix", "/k8s.ovs.com/ovs/network", "etcd prefix")
	opts.etcdKeyfile = flags.String("etcd-keyfile", "", "SSL key file used to secure etcd communication")
	opts.etcdCertfile = flags.String("etcd-certfile", "", "SSL certification file used to secure etcd communication")
	opts.etcdCAFile = flags.String("etcd-cafile", "", "SSL Certificate Authority file used to secure etcd communication")
	opts.etcdUsername = flags.String("etcd-username", "", "Username for BasicAuth to etcd")
	opts.etcdPassword = flags.String("etcd-password", "", "Password for BasicAuth to etcd")
	opts.network = flags.String("network", "", "network name, ex: (--network=test)")
	opts.hostname = flags.String("hostname", "", "Hostname")
	opts.dEndpoint = flags.String("docker-endpoints", "unix:///var/run/docker.sock", "endpoints to communicate with docker daemon")
	opts.version = flags.Bool("version", false, "print version and exit")
}

func main() {
	flag.Set("logtostderr", "true")
	flags.AddGoFlagSet(flag.CommandLine)
	flags.Parse(os.Args)

	if *opts.version {
		fmt.Fprintln(os.Stderr, version)
		os.Exit(0)
	}

	glog.Infof("Starting SDN daemon %v\n", version)

	var kubeClient *client.Client
	clientConfig := kubectl_util.DefaultClientConfig(flags)
	if cfg, err := clientConfig.ClientConfig(); err != nil {
		glog.Fatalf("Get kube config failed: %v", err)
	} else {
		kubeClient = client.NewOrDie(cfg)
	}

	cfg := &etcdv2.EtcdConfig{
		Endpoints: strings.Split(*opts.etcdEndpoints, ","),
		Keyfile:   *opts.etcdKeyfile,
		Certfile:  *opts.etcdCertfile,
		CAFile:    *opts.etcdCAFile,
		Prefix:    *opts.etcdPrefix,
		Username:  *opts.etcdUsername,
		Password:  *opts.etcdPassword,
	}

	eClient, err := etcdv2.NewManager(cfg)
	if err != nil {
		glog.Fatalf("Create etcd client failed: %v", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())

	hostname := *opts.hostname
	if hostname == "" {
		nodename, err := os.Hostname()
		if err != nil {
			glog.Fatalf("Get hostname failed: %v", err)
		}
		hostname = strings.ToLower(strings.TrimSpace(nodename))
		glog.Infof("Resolved hostname to %q", hostname)
	}

	dClient := dockertools.ConnectToDockerOrDie(*opts.dEndpoint, 10*time.Second)

	go ksdn.StartNode(kubeClient, eClient, dClient, *opts.network, hostname, ctx)

	fn := func(str string) {
		leader = str
		glog.V(5).Infof("Leader is %s, I am %s", str, hostname)
	}

	// Leader election for master.
	e, err := election.NewElection("k8s-ovs-worker", hostname, utils.SdnNamespace, 10*time.Second, fn, kubeClient)
	if err != nil {
		glog.Fatalf("Create election failed: %v", err)
	}
	go election.RunElection(e)

	backoff := utilwait.Backoff{
		Duration: 200 * time.Millisecond,
		Factor:   1.5,
		Steps:    10,
	}
	err = utilwait.ExponentialBackoff(backoff, func() (bool, error) {
		return leader != "", nil
	})
	if err != nil {
		glog.Fatalf("Leader election take too much time: %v", err)
	}

	go utilwait.PollInfinite(10*time.Second, func() (bool, error) {
		if leader == hostname {
			err := ksdn.StartMaster(kubeClient, eClient, *opts.network, ctx)
			if err != nil {
				glog.Fatalf("Start master failed%v\n", err)
			}
			return true, nil
		}
		return false, nil
	})

	daemon.SdNotify(false, "READY=1")

	<-sigs
	// unregister to get default OS nuke behaviour in case we don't exit cleanly
	signal.Stop(sigs)
	cancel()
}
