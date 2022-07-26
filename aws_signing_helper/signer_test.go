package aws_signing_helper

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws/request"
)

func setup() error {
	generateCertsScript := exec.Command("/bin/bash", "../generate-certs.sh")
	_, err := generateCertsScript.Output()
	if err != nil {
		return err
	}

	generateCredentialProcessDataScript := exec.Command("/bin/bash", "../generate-credential-process-data.sh")
	_, err = generateCredentialProcessDataScript.Output()
	if err != nil {
		return err
	}

	return nil
}

func TestMain(m *testing.M) {
	err := setup()
	if err != nil {
		log.Println(err.Error())
		os.Exit(1)
	}
	code := m.Run()
	os.Exit(code)
}

// Simple struct to define fixtures
type CertData struct {
	CertPath string
	KeyType  string
}

// Certificate fixtures should be generated by the script ./generate-certs.sh
// if they do not exist, or need to be updated.
func TestReadCertificateData(t *testing.T) {
	fixtures := []CertData{
		{"../tst/certs/ec-prime256v1-sha256-cert.pem", "EC"},
		{"../tst/certs/rsa-2048-sha256-cert.pem", "RSA"},
	}
	for _, fixture := range fixtures {
		certData, err := ReadCertificateData(fixture.CertPath)

		if err != nil {
			t.Log("Failed to read certificate data")
			t.Fail()
		}

		if certData.KeyType != fixture.KeyType {
			t.Logf("Wrong key type. Expected %s, got %s", fixture.KeyType, certData.KeyType)
			t.Fail()
		}
	}
}

func TestReadInvalidCertificateData(t *testing.T) {
	_, err := ReadCertificateData("../tst/certs/invalid-rsa-cert.pem")
	if err == nil || !strings.Contains(err.Error(), "could not parse certificate") {
		t.Log("Failed to throw a handled error")
		t.Fail()
	}
}

func TestReadCertificateBundleData(t *testing.T) {
	_, err := ReadCertificateBundleData("../tst/certs/cert-bundle.pem")
	if err != nil {
		t.Log("Failed to read certificate bundle data")
		t.Fail()
	}
}

func TestReadPrivateKeyData(t *testing.T) {
	fixtures := []string{
		"../tst/certs/ec-prime256v1-key.pem",
		"../tst/certs/ec-prime256v1-key-pkcs8.pem",
		"../tst/certs/rsa-2048-key.pem",
		"../tst/certs/rsa-2048-key-pkcs8.pem",
	}

	for _, fixture := range fixtures {
		_, err := ReadPrivateKeyData(fixture)

		if err != nil {
			t.Log(fixture)
			t.Log(err)
			t.Log("Failed to read private key data")
			t.Fail()
		}
	}
}

func TestReadInvalidPrivateKeyData(t *testing.T) {
	_, err := ReadPrivateKeyData("../tst/certs/invalid-rsa-key.pem")
	if err == nil || !strings.Contains(err.Error(), "unable to parse private key") {
		t.Log("Failed to throw a handled error")
		t.Fail()
	}
}

func TestBuildAuthorizationHeader(t *testing.T) {
	testRequest, err := http.NewRequest("POST", "https://rolesanywhere.us-west-2.amazonaws.com", nil)
	if err != nil {
		t.Log(err)
		t.Fail()
	}

	privateKey, _ := ReadPrivateKeyData("../tst/certs/rsa-2048-key.pem")
	certificateData, _ := ReadCertificateData("../tst/certs/rsa-2048-sha256-cert.pem")
	certificateDerData, _ := base64.StdEncoding.DecodeString(certificateData.CertificateData)
	certificate, _ := x509.ParseCertificate([]byte(certificateDerData))

	awsRequest := request.Request{HTTPRequest: testRequest}
	v4x509 := RolesAnywhereSigner{
		PrivateKey:  privateKey,
		Certificate: *certificate,
	}
	err = v4x509.SignWithCurrTime(&awsRequest)
	if err != nil {
		t.Log(err)
		t.Fail()
	}
}

// Verify that the provided payload was signed correctly with the provided options.
// This function is specifically used for unit testing.
func Verify(payload []byte, opts SigningOpts, sig []byte) (bool, error) {
	var hash []byte
	switch opts.Digest {
	case crypto.SHA256:
		sum := sha256.Sum256(payload)
		hash = sum[:]
	case crypto.SHA384:
		sum := sha512.Sum384(payload)
		hash = sum[:]
	case crypto.SHA512:
		sum := sha512.Sum512(payload)
		hash = sum[:]
	default:
		log.Fatal("Unsupported digest")
		return false, errors.New("Unsupported digest")
	}

	{
		privateKey, ok := opts.PrivateKey.(ecdsa.PrivateKey)
		if ok {
			valid := ecdsa.VerifyASN1(&privateKey.PublicKey, hash, sig)
			if valid {
				return valid, nil
			}
		}
	}

	{
		privateKey, ok := opts.PrivateKey.(rsa.PrivateKey)
		if ok {
			err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, opts.Digest, hash, sig)
			if err == nil {
				return true, nil
			}
		}
	}

	return false, nil
}

func TestSign(t *testing.T) {
	msg := "test message"

	var privateKeyList [2]crypto.PrivateKey
	{
		privateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		privateKeyList[0] = *privateKey
	}
	{
		privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		privateKeyList[1] = *privateKey
	}
	digestList := []crypto.Hash{crypto.SHA256, crypto.SHA384, crypto.SHA512}

	for _, privateKey := range privateKeyList {
		for _, digest := range digestList {
			signingResult, err := Sign([]byte(msg), SigningOpts{privateKey, digest})
			if err != nil {
				t.Log("Failed to sign the input message")
				t.Fail()
			}

			sig, err := hex.DecodeString(signingResult.Signature)
			if err != nil {
				t.Log("Failed to decode the hex-encoded signature")
				t.Fail()
			}
			valid, _ := Verify([]byte(msg), SigningOpts{privateKey, digest}, sig)
			if !valid {
				t.Log("Failed to verify the signature")
				t.Fail()
			}
		}
	}
}

func TestCredentialProcess(t *testing.T) {
	testTable := []struct {
		name   string
		server *httptest.Server
	}{
		{
			name: "create-session-server-response",
			server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{
					"credentialSet":[
					  {
						"assumedRoleUser": {
						"arn": "arn:aws:sts::000000000000:assumed-role/ExampleS3WriteRole",
						"assumedRoleId": "assumedRoleId"
						},
						"credentials":{
						  "accessKeyId": "accessKeyId",
						  "expiration": "2022-07-27T04:36:55Z",
						  "secretAccessKey": "secretAccessKey",
						  "sessionToken": "sessionToken"
						},
						"packedPolicySize": 10,
						"roleArn": "arn:aws:iam::000000000000:role/ExampleS3WriteRole",
						"sourceIdentity": "sourceIdentity"
					  }
					],
					"subjectArn": "arn:aws:rolesanywhere:us-east-1:000000000000:subject/41cl0bae-6783-40d4-ab20-65dc5d922e45"
				  }`))
			})),
		},
	}
	for _, tc := range testTable {
		credentialsOpts := CredentialsOpts{
			PrivateKeyId:      "../credential-process-data/client-key.pem",
			CertificateId:     "../credential-process-data/client-cert.pem",
			RoleArn:           "arn:aws:iam::000000000000:role/ExampleS3WriteRole",
			ProfileArnStr:     "arn:aws:rolesanywhere:us-east-1:000000000000:profile/41cl0bae-6783-40d4-ab20-65dc5d922e45",
			TrustAnchorArnStr: "arn:aws:rolesanywhere:us-east-1:000000000000:trust-anchor/41cl0bae-6783-40d4-ab20-65dc5d922e45",
			Endpoint:          tc.server.URL,
			SessionDuration:   900,
		}
		t.Run(tc.name, func(t *testing.T) {
			defer tc.server.Close()
			resp, err := GenerateCredentials(&credentialsOpts)

			if err != nil {
				t.Log(err)
				t.Log("Unable to call credential-process")
				t.Fail()
			}

			if resp.AccessKeyId != "accessKeyId" {
				t.Log("Incorrect access key id")
				t.Fail()
			}
			if resp.SecretAccessKey != "secretAccessKey" {
				t.Log("Incorrect secret access key")
				t.Fail()
			}
			if resp.SessionToken != "sessionToken" {
				t.Log("Incorrect session token")
				t.Fail()
			}
		})
	}
}
