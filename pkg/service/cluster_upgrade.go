package service

import (
	"errors"
	"github.com/KubeOperator/KubeOperator/pkg/constant"
	"github.com/KubeOperator/KubeOperator/pkg/db"
	"github.com/KubeOperator/KubeOperator/pkg/dto"
	"github.com/KubeOperator/KubeOperator/pkg/model"
	"github.com/KubeOperator/KubeOperator/pkg/repository"
	"github.com/KubeOperator/KubeOperator/pkg/service/cluster/adm"
	"github.com/KubeOperator/KubeOperator/pkg/service/cluster/adm/phases/upgrade"
	"github.com/KubeOperator/KubeOperator/pkg/util/ansible"
	"io"
	"time"
)

type ClusterUpgradeService interface {
	Upgrade(upgrade dto.ClusterUpgrade) error
}

func NewClusterUpgradeService() ClusterUpgradeService {
	return &clusterUpgradeService{
		clusterService:    NewClusterService(),
		clusterStatusRepo: repository.NewClusterStatusRepository(),
	}
}

type clusterUpgradeService struct {
	clusterService    ClusterService
	clusterStatusRepo repository.ClusterStatusRepository
}

func (c clusterUpgradeService) Upgrade(upgrade dto.ClusterUpgrade) error {
	clusterDTO, err := c.clusterService.Get(upgrade.ClusterName)
	if err != nil {
		return err
	}
	cluster := clusterDTO.Cluster
	err = c.prepareUpgrade(&cluster)
	if err != nil {
		return err
	}
	// 创建日志
	logId, writer, err := ansible.CreateAnsibleLogWriter(cluster.Name)
	if err != nil {
		return err
	}
	cluster.LogId = logId
	if err := db.DB.Save(&cluster).Error; err != nil {
		return err
	}
	// 变更状态为升级状态
	// 1. 清空原来的condition 2. 变更状态为更新状态
	if len(cluster.Status.ClusterStatusConditions) > 0 && cluster.Status.ClusterStatusConditions[0].Name == "UpgradeCluster" {
		cluster.Status.ClusterStatusConditions[0].Status = constant.ConditionUnknown
	} else {
		cluster.Status.ClusterStatusConditions = []model.ClusterStatusCondition{}
	}
	cluster.Status.Phase = constant.ClusterUpgrading
	if err = c.clusterStatusRepo.Save(&cluster.Status); err != nil {
		return err
	}
	go c.do(&cluster, writer, upgrade.Version)
	return nil
}

/*
检查是否符合升级的条件
*/
func (c clusterUpgradeService) prepareUpgrade(cluster *model.Cluster) error {
	if cluster.Source != constant.ClusterSourceLocal {
		return errors.New("CLUSTER_IS_NOT_LOCAL")
	}
	return nil
}

func (c clusterUpgradeService) do(cluster *model.Cluster, writer io.Writer, version string) {
	admCluster := adm.NewCluster(*cluster)
	p := &upgrade.UpgradeClusterPhase{
		Version: version,
	}
	condition := model.ClusterStatusCondition{
		Name:          "UpgradeCluster",
		Status:        constant.ConditionUnknown,
		OrderNum:      0,
		LastProbeTime: time.Now(),
	}
	cluster.Status.ClusterStatusConditions = append(cluster.Status.ClusterStatusConditions, condition)
	_ = c.clusterStatusRepo.Save(&cluster.Status)
	if err := p.Run(admCluster.Kobe, writer); err != nil {
		cluster.Status.Phase = constant.ClusterFailed
		cluster.Status.Message = err.Error()
		cluster.Status.ClusterStatusConditions[len(cluster.Status.ClusterStatusConditions)-1].Status = constant.ConditionFalse
		cluster.Status.ClusterStatusConditions[len(cluster.Status.ClusterStatusConditions)-1].Message = err.Error()
		_ = c.clusterStatusRepo.Save(&cluster.Status)
		return
	}
	cluster.Status.ClusterStatusConditions[len(cluster.Status.ClusterStatusConditions)-1].Status = constant.ConditionTrue
	cluster.Status.Phase = constant.ClusterRunning
	_ = c.clusterStatusRepo.Save(&cluster.Status)
	cluster.Spec.Version = version
	db.DB.Save(&cluster.Spec)

}
