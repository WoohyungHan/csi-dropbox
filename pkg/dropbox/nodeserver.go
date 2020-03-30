package dropbox

import (
	"bufio"
	"bytes"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/utils/mount"
	"os"
	"os/exec"
	"path"
	"strings"
)

type nodeServer struct {
	nodeID string
}

func NewNodeServer(nodeId string) *nodeServer {
	return &nodeServer{
		nodeID: nodeId,
	}
}

const (
	rootDir = "/mnt/csi-dropbox"
	dataDir = rootDir + "/data"
)

func (n *nodeServer) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: n.nodeID,
	}, nil
}

func (n nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(req.GetStagingTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capability missing in request")
	}
	token, exists := req.Secrets["token"]
	if !exists {
		return nil, status.Error(codes.InvalidArgument, "Token not exists")
	}

	glog.Infof("targetPath: %v", req.GetStagingTargetPath())
	glog.Infof("dataDir: %v", dataDir)

	err := os.MkdirAll(dataDir, 0777)
	if err != nil {
		glog.Error("Can't create dataDir %s", dataDir)
		return nil, err
	}

	dbxfsConfigPath := path.Join(rootDir, "dbxfs_config.json")
	dbxfsTokenPath := path.Join(rootDir, "dbxfs_token")

	err = writeFile(dbxfsConfigPath, "{\"access_token_command\": [\"cat\", \""+dbxfsTokenPath+"\"], \"send_error_reports\": true, \"asked_send_error_reports\": true}")
	if err != nil {
		glog.Error("Can't create dbxfs config file")
		return nil, err
	}

	err = writeFile(dbxfsTokenPath, token)
	if err != nil {
		glog.Error("Can't create dbxfs token file")
		return nil, err
	}

	cmd := exec.Command("dbxfs", dataDir, "-c", dbxfsConfigPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		glog.Errorf("Cant mount dbxfs: %s %s", stdout.String(), stderr.String())
		return nil, err
	}
	glog.V(4).Infof("dropbox-csi: volume %s is mounted %s", dataDir, stdout.String())

	return &csi.NodeStageVolumeResponse{}, nil
}

func writeFile(path, contents string) error {
	outfile, err := os.Create(path)
	if err != nil {
		glog.Error("Can't create %s", path)
		return err
	}

	writer := bufio.NewWriter(outfile)
	_, err = writer.WriteString(contents)
	if err != nil {
		glog.Error("Can't write %s", path)
		return err
	}

	writer.Flush()
	outfile.Close()

	return nil
}

func (n nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(req.GetStagingTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	err := mount.New("").Unmount(dataDir)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	glog.V(4).Infof("dropbox-csi: volume %s is unmounted,", dataDir)

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (n nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	targetPath := req.GetTargetPath()

	notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(targetPath, 0750); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			notMnt = true
		} else {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if !notMnt {
		return &csi.NodePublishVolumeResponse{}, nil
	}

	options := []string{"bind"}
	if req.GetReadonly() {
		options = append(options, "ro")
	}

	dirToMountInDropbox := dataDir
	if len(req.VolumeContext["path"]) != 0 {
		dirToMountInDropbox = path.Join(dirToMountInDropbox, req.VolumeContext["path"])
	}

	mounter := mount.New("")
	if err := mounter.Mount(dirToMountInDropbox, targetPath, "", options); err != nil {
		var errList strings.Builder
		errList.WriteString(err.Error())
	}
	glog.V(4).Infof("dropbox-csi: volume %s is mount to %s.", dirToMountInDropbox, targetPath)

	return &csi.NodePublishVolumeResponse{}, nil
}

func (n nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	targetPath := req.GetTargetPath()

	err := mount.New("").Unmount(targetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	glog.V(4).Infof("dropbox-csi: volume %s is unmounted,", targetPath)

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (n *nodeServer) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (n nodeServer) NodeGetVolumeStats(context.Context, *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	panic("implement me node volumestats")
}

func (n nodeServer) NodeExpandVolume(context.Context, *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	panic("implement me node expand")
}
