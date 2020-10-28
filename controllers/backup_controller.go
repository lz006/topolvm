package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	topolvmv1 "github.com/topolvm/topolvm/api/v1"
	"github.com/topolvm/topolvm/lvmd/proto"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// BackupReconciler reconciles a Backup object on each node.
type BackupReconciler struct {
	client.Client
	log       logr.Logger
	nodeName  string
	lvService proto.LVServiceClient
}

// +kubebuilder:rbac:groups=topolvm.cybozu.com,resources=backups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=topolvm.cybozu.com,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch

// NewBackupReconciler returns BackupReconciler
func NewBackupReconciler(client client.Client, log logr.Logger, nodeName string, conn *grpc.ClientConn) *BackupReconciler {
	return &BackupReconciler{
		Client:    client,
		log:       log,
		nodeName:  nodeName,
		lvService: proto.NewLVServiceClient(conn),
	}
}

// Reconcile backup for a LogicalVolume / PVC.
func (r *BackupReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.log.WithValues("Backup", req.NamespacedName)

	log.Info("Backup reconcile triggered")

	bu := new(topolvmv1.Backup)
	if err := r.Get(ctx, req.NamespacedName, bu); err != nil {
		if !apierrs.IsNotFound(err) {
			log.Error(err, "unable to fetch Backup")
			return ctrl.Result{Requeue: false}, nil
		}
		return ctrl.Result{Requeue: false}, nil
	}

	// Prevent endless loops
	if bu.Status.State == topolvmv1.Error || bu.Status.State == topolvmv1.Complete {
		return ctrl.Result{Requeue: false}, nil
	}

	if strings.ToLower(bu.Spec.Kind) == "pvc" {
		pvc := new(corev1.PersistentVolumeClaim)
		if err := r.Get(ctx, types.NamespacedName{Namespace: bu.GetNamespace(), Name: bu.Spec.Name}, pvc); err != nil {
			bu.Status.State = topolvmv1.Error
			bu.Status.Message = fmt.Sprintf("%s: %s", r.nodeName, err.Error())
			r.Status().Update(ctx, bu)

			if !apierrs.IsNotFound(err) {
				log.Error(err, "unable to fetch PVC")
				return ctrl.Result{}, err
			}
			log.Error(err, err.Error())
			return ctrl.Result{}, err
		}

		log.Info("Backup: PVC found")

		pv := new(corev1.PersistentVolume)
		if err := r.Get(ctx, types.NamespacedName{Namespace: "", Name: pvc.Spec.VolumeName}, pv); err != nil {
			bu.Status.State = topolvmv1.Error
			bu.Status.Message = fmt.Sprintf("%s: %s", r.nodeName, err.Error())
			r.Status().Update(ctx, bu)

			if !apierrs.IsNotFound(err) {
				log.Error(err, "unable to fetch PV")
				return ctrl.Result{}, err
			}
			log.Error(err, err.Error())
			return ctrl.Result{}, err
		}

		// Check if volume can be backed up
		if pv.Spec.CSI.Driver != "topolvm.cybozu.com" || *pv.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
			err := errors.New(string("Backup Controller: unsupported backup type"))
			bu.Status.State = topolvmv1.Error
			bu.Status.Message = fmt.Sprintf("%s: %s", r.nodeName, err.Error())
			r.Status().Update(ctx, bu)

			log.Error(err, "requested", map[string]interface{}{
				"driver":     pv.Spec.CSI.Driver,
				"volumeMode": *pv.Spec.VolumeMode,
			})
			return ctrl.Result{}, err
		}

		// Check if volume sits on the same node
		if pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Values[0] != r.nodeName {
			log.Info("Backup: This node is not in charge for backup creation", map[string]interface{}{
				"ownNode":    r.nodeName,
				"volumeNode": pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Values[0],
			})
			return ctrl.Result{}, nil
		}

		// Get Secret
		secret := new(corev1.Secret)
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: bu.GetNamespace(), Name: bu.Spec.S3.Secret}, secret); err != nil {
			bu.Status.State = topolvmv1.Error
			bu.Status.Message = fmt.Sprintf("%s: %s", r.nodeName, err.Error())
			r.Status().Update(ctx, bu)

			log.Error(err, "Secret cannot retrieved", map[string]interface{}{
				"name":      bu.Spec.S3.Secret,
				"namespace": bu.GetNamespace(),
			})

			return ctrl.Result{}, err
		}

		log.Info("Backup: PV found", "lv", pv.Spec.CSI.VolumeHandle)
		// Now call lvmd with VolumeHandle (lv-id)
		// lvmd then in turn triggers backup script

		createBackupReq := proto.CreateBackupRequest{
			Backup: &proto.Backup{
				Name:         bu.GetName(),
				VolumeHandle: pv.Spec.CSI.VolumeHandle,
				DataSource: &proto.DataSource{
					SynchronousRestore: false, // doesn't affect something as we are in backup process
					Type: &proto.DataSource_S3_{
						S3: &proto.DataSource_S3{
							Path:            bu.Spec.S3.Path,
							Endpoint:        bu.Spec.S3.Endpoint,
							VerifyTls:       bu.Spec.S3.VerifyTLS,
							HttpProxy:       bu.Spec.S3.HTTPProxy,
							HttpsProxy:      bu.Spec.S3.HTTPSProxy,
							AccessKeyId:     string(secret.Data["AccessKeyId"]),
							SecretAccessKey: string(secret.Data["SecretAccessKey"]),
							SessionToken:    string(secret.Data["SessionToken"]),
							EncryptionKey:   string(secret.Data["EncryptionKey"]),
						},
					},
				},
			},
		}

		response, err := r.lvService.CreateBackup(ctx, &createBackupReq)
		if err != nil {
			bu.Status.State = topolvmv1.Error
			bu.Status.Message = fmt.Sprintf("%s: %s", r.nodeName, err.Error())
			r.Status().Update(ctx, bu)
			return ctrl.Result{}, nil
		}
		bu.Status.Message = fmt.Sprintf("%s: %s", r.nodeName, response.BackupState.GetMsg())
		if response.BackupState.GetState() == proto.StateType_INPROGRESS {
			bu.Status.State = topolvmv1.InProgress
			r.Status().Update(ctx, bu)
			return reconcile.Result{
				RequeueAfter: 30 * time.Second,
				Requeue:      true,
			}, nil
		} else if response.BackupState.GetState() == proto.StateType_ERROR {
			bu.Status.State = topolvmv1.Error
			r.Status().Update(ctx, bu)
			return ctrl.Result{}, errors.New(string(response.BackupState.GetMsg()))
		} else if response.BackupState.GetState() == proto.StateType_COMPLETE {
			bu.Status.State = topolvmv1.Complete
		}
		r.Status().Update(ctx, bu)

	} else {
		log.Error(errors.New(string("Backup Controller: unsupported backup type")), "requested", map[string]interface{}{
			"kind": strings.ToLower(bu.Spec.Kind),
		})
	}

	return ctrl.Result{}, nil

}

// SetupWithManager sets up Reconciler with Manager.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Add filter for backups that aren't in state "completed"
	// and ignore Delete & Update events
	return ctrl.NewControllerManagedBy(mgr).
		For(&topolvmv1.Backup{}).
		WithEventFilter(&backupFilter{}).
		Complete(r)
}

type backupFilter struct {
}

// func (f backupFilter) filter(lv *topolvmv1.Backup) bool {
// 	if lv == nil {
// 		return false
// 	}
// 	if lv.Spec.NodeName == f.nodeName {
// 		return true
// 	}
// 	return false
// }

func (f backupFilter) Create(e event.CreateEvent) bool {
	return true
}

func (f backupFilter) Delete(e event.DeleteEvent) bool {
	return true
}

func (f backupFilter) Update(e event.UpdateEvent) bool {
	return false
}

func (f backupFilter) Generic(e event.GenericEvent) bool {
	return false
}
