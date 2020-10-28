package lvmd

import (
	"context"

	"github.com/cybozu-go/log"
	"github.com/topolvm/topolvm/lvmd/backup"
	"github.com/topolvm/topolvm/lvmd/command"
	"github.com/topolvm/topolvm/lvmd/proto"
	"github.com/topolvm/topolvm/lvmd/restore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewLVService creates a new LVServiceServer
func NewLVService(mapper *DeviceClassManager, notifyFunc func(), backupConf *backup.BaseConf, restoreConf *restore.BaseConf) proto.LVServiceServer {
	return &lvService{
		mapper:     mapper,
		notifyFunc: notifyFunc,
		backup:     backupConf,
		restore:    restoreConf,
	}
}

type lvService struct {
	proto.UnimplementedLVServiceServer
	mapper     *DeviceClassManager
	notifyFunc func()
	backup     *backup.BaseConf
	restore    *restore.BaseConf
}

func (s *lvService) notify() {
	if s.notifyFunc == nil {
		return
	}
	s.notifyFunc()
}

func (s *lvService) CreateLV(_ context.Context, req *proto.CreateLVRequest) (*proto.CreateLVResponse, error) {
	dc, err := s.mapper.DeviceClass(req.DeviceClass)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%s: %s", err.Error(), req.DeviceClass)
	}
	vg, err := command.FindVolumeGroup(dc.VolumeGroup)
	if err != nil {
		return nil, err
	}
	requested := req.GetSizeGb() << 30
	free, err := vg.Free()
	if err != nil {
		log.Error("failed to free VG", map[string]interface{}{
			log.FnError: err,
		})
		return nil, status.Error(codes.Internal, err.Error())
	}

	if free < requested {
		log.Error("no enough space left on VG", map[string]interface{}{
			"free":      free,
			"requested": requested,
		})
		return nil, status.Errorf(codes.ResourceExhausted, "no enough space left on VG: free=%d, requested=%d", free, requested)
	}

	var lv *command.LogicalVolume
	dataSource := req.GetDataSource()

	if dataSource != nil {
		lv, err = vg.CreateVolumeFromSource(req.GetName(), requested, req.GetTags(), dataSource, s.restore)
	} else {
		lv, err = vg.CreateVolume(req.GetName(), requested, req.GetTags())
	}

	if err != nil {
		log.Error("failed to create volume", map[string]interface{}{
			"name":      req.GetName(),
			"requested": requested,
			"tags":      req.GetTags(),
		})
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.notify()

	log.Info("created a new LV", map[string]interface{}{
		"name": req.GetName(),
		"size": requested,
	})

	return &proto.CreateLVResponse{
		Volume: &proto.LogicalVolume{
			Name:     lv.Name(),
			SizeGb:   lv.Size() >> 30,
			DevMajor: lv.MajorNumber(),
			DevMinor: lv.MinorNumber(),
		},
	}, nil
}

func (s *lvService) RemoveLV(_ context.Context, req *proto.RemoveLVRequest) (*proto.Empty, error) {
	dc, err := s.mapper.DeviceClass(req.DeviceClass)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%s: %s", err.Error(), req.DeviceClass)
	}
	vg, err := command.FindVolumeGroup(dc.VolumeGroup)
	if err != nil {
		return nil, err
	}
	lvs, err := vg.ListVolumes()
	if err != nil {
		log.Error("failed to list volumes", map[string]interface{}{
			log.FnError: err,
		})
		return nil, status.Error(codes.Internal, err.Error())
	}

	for _, lv := range lvs {
		if lv.Name() != req.GetName() {
			continue
		}

		err = lv.Remove()
		if err != nil {
			log.Error("failed to remove volume", map[string]interface{}{
				log.FnError: err,
				"name":      lv.Name(),
			})
			return nil, status.Error(codes.Internal, err.Error())
		}
		s.notify()

		log.Info("removed a LV", map[string]interface{}{
			"name": req.GetName(),
		})
		break
	}

	return &proto.Empty{}, nil
}

func (s *lvService) ResizeLV(_ context.Context, req *proto.ResizeLVRequest) (*proto.Empty, error) {
	dc, err := s.mapper.DeviceClass(req.DeviceClass)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%s: %s", err.Error(), req.DeviceClass)
	}
	vg, err := command.FindVolumeGroup(dc.VolumeGroup)
	if err != nil {
		return nil, err
	}
	lv, err := vg.FindVolume(req.GetName())
	if err == command.ErrNotFound {
		log.Error("logical volume is not found", map[string]interface{}{
			log.FnError: err,
			"name":      req.GetName(),
		})
		return nil, status.Errorf(codes.NotFound, "logical volume %s is not found", req.GetName())
	}
	if err != nil {
		log.Error("failed to find volume", map[string]interface{}{
			log.FnError: err,
			"name":      req.GetName(),
		})
		return nil, status.Error(codes.Internal, err.Error())
	}

	requested := req.GetSizeGb() << 30
	current := lv.Size()

	if requested < current {
		log.Error("shrinking volume size is not allowed", map[string]interface{}{
			log.FnError: err,
			"name":      req.GetName(),
			"requested": requested,
			"current":   current,
		})
		return nil, status.Error(codes.OutOfRange, "shrinking volume size is not allowed")
	}

	free, err := vg.Free()
	if err != nil {
		log.Error("failed to free VG", map[string]interface{}{
			log.FnError: err,
			"name":      req.GetName(),
		})
		return nil, status.Error(codes.Internal, err.Error())
	}
	if free < (requested - current) {
		log.Error("no enough space left on VG", map[string]interface{}{
			log.FnError: err,
			"name":      req.GetName(),
			"requested": requested,
			"current":   current,
			"free":      free,
		})
		return nil, status.Errorf(codes.ResourceExhausted, "no enough space left on VG: free=%d, requested=%d", free, requested-current)
	}

	err = lv.Resize(requested)
	if err != nil {
		log.Error("failed to resize LV", map[string]interface{}{
			log.FnError: err,
			"name":      req.GetName(),
			"requested": requested,
			"current":   current,
			"free":      free,
		})
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.notify()

	log.Info("resized a LV", map[string]interface{}{
		"name": req.GetName(),
		"size": requested,
	})

	return &proto.Empty{}, nil
}

func (s lvService) CreateBackup(_ context.Context, req *proto.CreateBackupRequest) (*proto.CreateBackupResponse, error) {
	log.Info("Backup creation requested : ", map[string]interface{}{
		"name":   req.GetBackup().GetName(),
		"volume": req.GetBackup().GetVolumeHandle(),
	})
	backupState := proto.BackupState{
		Name:  "",
		State: proto.StateType_ERROR,
		Msg:   "",
	}

	// Call via nohup script for backup creation
	if status, err := command.CreateBackup(req.Backup, s.backup); err != nil {
		log.Error("Backup creation failed", map[string]interface{}{
			"OS error": err.Error(),
		})
		backupState.Msg = err.Error()
		backupState.State = proto.StateType_ERROR
		backupState.Name = req.Backup.Name
	} else {
		backupState.Msg = ""
		backupState.State = status
		backupState.Name = req.Backup.Name
	}

	// Check for result files
	// TODO

	// Create response
	return &proto.CreateBackupResponse{
		BackupState: &backupState,
	}, nil
}
