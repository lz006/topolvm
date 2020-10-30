# Create Pre-Populated Volumes using VolumeContentSource
``` bash
#go get sigs.k8s.io/controller-tools/cmd/controller-gen@v0.2.7

make setup
```


``` bash
# copy csi-provisioner binary first

# regenerate manifests if api is changed
make generate
make manifests

# build lvmd
make TOPOLVM_VERSION=0.6.0-br-rev1 build/lvmd && \
chmod 777 build/lvmd
scp ./build/lvmd root@10.0.0.67:/usr/local/bin/lvmd

# build hypertopolvm PROD
make build/hypertopolvm && \
make IMAGE_PREFIX=core.harbor.hub.sulzer.de/iac/ IMAGE_BUILD_ARGS='--build-arg="http_proxy=http://172.21.0.10:88"' image && \
make tag IMAGE_PREFIX=core.harbor.hub.sulzer.de/iac/ IMAGE_TAG=0.6.0-br-rev1 && \
docker push core.harbor.hub.sulzer.de/iac/topolvm:0.6.0-br-1

# build hypertopolvm DEV
make build/hypertopolvm-dev && \
mv -f build/hypertopolvm-dev build/hypertopolvm && \
make IMAGE_PREFIX=core.harbor.hub.sulzer.de/iac/ IMAGE_BUILD_ARGS='--build-arg="http_proxy=http://172.21.0.10:88"' image && \
make tag IMAGE_PREFIX=core.harbor.hub.sulzer.de/iac/ IMAGE_TAG=0.1.27 && \
docker push core.harbor.hub.sulzer.de/iac/topolvm:0.1.27

rsync -avz test_manifests/ root@10.0.0.66:/root/kube-manifests/
```

## TODO
1. Make topolvm-node transfer all params to lvmd
2. Build backup reconciler
3. Setup Minio using podman
4. Integrate Minio with keycloak
5. Create upload shell script
6. Create download shell script


## References
- https://github.com/kubernetes/enhancements/commit/933a044fa4efc137cd6a6479c96cbf627a4e92cc
- https://github.com/kubernetes-csi/external-provisioner
- https://github.com/kubernetes-csi/csi-driver-host-path/blob/master/pkg/hostpath/controllerserver.go
- https://github.com/topolvm/topolvm/blob/master/docs/design.md
- https://github.com/kubernetes-csi/external-snapshotter/blob/master/pkg/apis/volumesnapshot/v1beta1/types.go

## Design Decision
- According to CSI docs (https://kubernetes-csi.github.io/docs/snapshot-restore-feature.html) snapshots are point in time copy of a volume that allows restoring a state of a volume or act as source for creation of pre-populated volumes. In my opinion former use case is more related to situations where same underlying storage system handles snapshot and regular volumes. The comment "VolumeSnapshotContent represents the actual "on-disk" snapshot object in the underlying storage system" in source code of type "VolumeSnapshotContent" (https://github.com/kubernetes-csi/external-snapshotter/blob/master/pkg/apis/volumesnapshot/v1beta1/types.go) points in same direction. Furthermore we expect restoring to a snapshot is only a matter of several seconds or less (at least for COW-based storage systems). Here this assumption is only true for a short time when a backup is invoked. Such a "Backup" object triggers creation of a lvm snapshot that lives as long as it takes transfering its content to a third party system (S3 Backend). As soon as this job is finished, there is no relation between backup (snapshot) and backed pv that kubernetes understands. This is why I decided to not make use of type "VolumeSnapshot".


## Tests
``` bash
# Transfer manifests
rsync -a test_manifests/ root@10.0.0.66:/tmp/
```


## Remote Debugging
``` bash
# First forward port to localhost
ssh -L 2345:127.0.0.1:2345 root@10.0.0.67

# Attach to binary
dlv attach --headless --api-version=2 --listen=:2345 $(pgrep lvmd)



# node
ssh -L 6443:127.0.0.1:6443 root@10.0.0.66

kubectl --kubeconfig .kube/config_hetzner_topo port-forward -n topolvm-system pod/node-94698 2345:2345

docker exec -ti $( docker ps | grep k8s_topolvm-node_node | awk '{print $1}') bash
export https_proxy=http://proxy.sulzercloud.de:8888 && \
export http_proxy=http://proxy.sulzercloud.de:8888 && \
apt update -y && \
apt install -y git && \
cd /tmp && \
curl -L -o go.tar.gz https://golang.org/dl/go1.14.7.linux-amd64.tar.gz && \
tar -C /usr/local -xvf go.tar.gz && \
ln -s /usr/local/go/bin/go /bin/go && \
ln -s /usr/local/go/bin/gofmt /bin/gofmt && \
rm -f go.tar.gz && \
go get github.com/go-delve/delve/cmd/dlv && \
/root/go/bin/dlv attach --headless --api-version=2 --listen=:2345 1

# Debug Mode
## Local
rsync -a --exclude 'build' --exclude 'bin' . root@10.0.0.67:/opt/lvmd/
## Remote
yum install -y gcc git
https_proxy=http://proxy.sulzercloud.de:8888 go get all
https_proxy=http://proxy.sulzercloud.de:8888 go get github.com/ramya-rao-a/go-outline
# Install extension 'Remote Development'
# Press 'F1'
# Type and select 'Remote-SSH: Connect to Host'
# Enter 'root@10.0.0.67'

```
launch.json
``` json 
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "lvmd remote",
            "type": "go",
            "request": "attach",
            "remotePath": "${workspaceFolder}",
            "mode": "remote",
            "port": 2345,
            "host": "127.0.0.1"
        },
        {
            "name": "lvmd local",
            "type": "go",
            "request": "launch",
            "mode": "auto",
            "program": "${fileDirname}",
            "args": ["--config=/etc/topolvm/lvmd.yaml"]
        },
        {
            "name": "node local",
            "type": "go",
            "request": "launch",
            "mode": "auto",
            "program": "${fileDirname}",
            "args": ["/topolvm-node", "--lvmd-socket=/run/topolvm/lvmd.sock", "--csi-socket=/var/lib/kubelet/plugins/topolvm.cybozu.com/node/csi-topolvm.sock"],
            "env": {
                "NODE_NAME": "node2"
            }
        }
    ]
}

```

## Program Execution Plan
csi-provisioner (different repo)
1. controller.go: "Provision()" calls grpc service "CreateVolume()"


topolvm-controller
1. controller.go: "CreateVolume()" calls "CreateVolume()" or "CreateVolumeFromSource()"
2. logicalvolume_service.go: "CreateVolume()" or "CreateVolumeFromSource()" creates LogicalVolume object


topolvm-node
1. logicalvolume_controller.go: "Reconcile()" watches LogicalVolume objects and calls "createLV()" in case of new object in same file
2. logicalvolume_controller.go: "createLV()" calls grpc service "CreateLV()" that is implemented by lvmd


lvmd
1. lvservice.go: "CreateLV()" calls "CreateVolume()" or "CreateVolumeFromSource()"
2. lvm.go: "CreateVolume()" or "CreateVolumeFromSource()" creates actual LVM logial volume



