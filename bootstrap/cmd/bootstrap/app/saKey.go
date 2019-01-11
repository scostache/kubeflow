package app

import (
	iamadmin "cloud.google.com/go/iam/admin/apiv1"
	"encoding/base64"
	"fmt"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/iam/admin/v1"
	"k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

const OauthSecretName = "kubeflow-oauth"

func (s *ksServer) ConfigCluster(ctx context.Context, req CreateRequest) error {
	k8sConfig, err := buildClusterConfig(ctx, req.Token, req.Project, req.Zone, req.Cluster)
	if err != nil {
		log.Errorf("Failed getting GKE cluster config: %v", err)
		return err
	}
	k8sClientset, err := clientset.NewForConfig(k8sConfig)
	if err != nil {
		return err
	}
	log.Info("Creating namespace")
	if err := CreateNamespace(&req, k8sClientset); err != nil {
		log.Errorf("Failed to create namespace: %v", err)
		return err
	}
	log.Info("Inserting oauth credentails")
	if err := InsertOauthCredentails(&req, k8sClientset); err != nil {
		return err
	}
	log.Infof("Inserting sa keys...")
	if err := s.InsertSaKeys(ctx, &req, k8sClientset); err != nil {
		log.Errorf("Failed to insert service account key: %v", err)
		return err
	}
	return nil
}

func CreateNamespace(req *CreateRequest, k8sClientset *clientset.Clientset) error {
	_, err := k8sClientset.CoreV1().Namespaces().Create(
		&v1.Namespace{
			ObjectMeta: meta_v1.ObjectMeta{
				Name: req.Namespace,
			},
		},
	)
	return err
}

func InsertOauthCredentails(req *CreateRequest, k8sClientset *clientset.Clientset) error {
	secretData := make(map[string][]byte)
	ClientIdData, err := base64.StdEncoding.DecodeString(req.ClientId)
	if err != nil {
		log.Errorf("Failed decoding client id: %v", err)
		return err
	}
	ClientSecretData, err := base64.StdEncoding.DecodeString(req.ClientSecret)
	if err != nil {
		log.Errorf("Failed decoding client secret: %v", err)
		return err
	}
	secretData["client_id"] = ClientIdData
	secretData["client_secret"] = ClientSecretData
	_, err = k8sClientset.CoreV1().Secrets(req.Namespace).Create(
		&v1.Secret{
			ObjectMeta: meta_v1.ObjectMeta{
				Namespace: req.Namespace,
				Name:      OauthSecretName,
			},
			Data: secretData,
		})
	if err != nil {
		log.Errorf("Failed creating oauth credentails in GKE cluster: %v", err)
		return err
	}
	return nil
}

func (s *ksServer) InsertSaKeys(ctx context.Context, req *CreateRequest, k8sClientset *clientset.Clientset) error {
	err := s.InsertSaKey(ctx, req, "admin-gcp-sa.json", "admin-gcp-sa",
		fmt.Sprintf("%v-admin@%v.iam.gserviceaccount.com", req.Cluster, req.Project), k8sClientset)
	if err != nil {
		return err
	}
	err = s.InsertSaKey(ctx, req, "user-gcp-sa.json", "user-gcp-sa",
		fmt.Sprintf("%v-user@%v.iam.gserviceaccount.com", req.Cluster, req.Project), k8sClientset)
	return err
}

func (s *ksServer) InsertSaKey(ctx context.Context, request *CreateRequest, secretKey string,
	secretName string, serviceAccount string, k8sClientset *clientset.Clientset) error {
	ts := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: request.Token,
	})

	c, err := iamadmin.NewIamClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		log.Errorf("Cannot create iam admin client: %v", err)
		return err
	}
	createServiceAccountKeyRequest := admin.CreateServiceAccountKeyRequest{
		Name: fmt.Sprintf("projects/%v/serviceAccounts/%v", request.Project, serviceAccount),
	}

	projLock := s.GetProjectLock(request.Project)
	projLock.Lock()
	defer projLock.Unlock()

	createdKey, err := c.CreateServiceAccountKey(ctx, &createServiceAccountKeyRequest)
	if err != nil {
		log.Errorf("Failed creating sa key: %v", err)
		return err
	}
	secretData := make(map[string][]byte)
	secretData[secretKey] = createdKey.PrivateKeyData
	_, err = k8sClientset.CoreV1().Secrets(request.Namespace).Create(
		&v1.Secret{
			ObjectMeta: meta_v1.ObjectMeta{
				Namespace: request.Namespace,
				Name:      secretName,
			},
			Data: secretData,
		})
	if err != nil {
		log.Errorf("Failed creating secret in GKE cluster: %v", err)
		return err
	}
	return nil
}
