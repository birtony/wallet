/*
Copyright SecureKey Technologies Inc. All Rights Reserved.
Copyright Avast Software. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/hyperledger/aries-framework-go/pkg/crypto/tinkcrypto"
	"github.com/hyperledger/aries-framework-go/pkg/doc/util/didsignjwt"
	"github.com/hyperledger/aries-framework-go/pkg/kms/localkms"
	"github.com/hyperledger/aries-framework-go/pkg/secretlock/noop"
	"github.com/hyperledger/aries-framework-go/pkg/vdr/key"

	"github.com/hyperledger/aries-framework-go/pkg/doc/signature/jsonld"
	"github.com/hyperledger/aries-framework-go/pkg/doc/signature/suite"
	"github.com/hyperledger/aries-framework-go/pkg/doc/signature/suite/ed25519signature2018"

	"github.com/btcsuite/btcutil/base58"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/piprate/json-gold/ld"
	"github.com/square/go-jose/jwt"

	"github.com/hyperledger/aries-framework-go/component/storageutil/mem"
	"github.com/hyperledger/aries-framework-go/pkg/client/didexchange"
	"github.com/hyperledger/aries-framework-go/pkg/client/outofband"
	"github.com/hyperledger/aries-framework-go/pkg/client/outofbandv2"
	"github.com/hyperledger/aries-framework-go/pkg/client/presentproof"
	"github.com/hyperledger/aries-framework-go/pkg/common/log"
	cryptoapi "github.com/hyperledger/aries-framework-go/pkg/crypto"
	"github.com/hyperledger/aries-framework-go/pkg/didcomm/common/service"
	"github.com/hyperledger/aries-framework-go/pkg/didcomm/protocol/decorator"
	"github.com/hyperledger/aries-framework-go/pkg/didcomm/protocol/issuecredential"
	presentproofsvc "github.com/hyperledger/aries-framework-go/pkg/didcomm/protocol/presentproof"
	"github.com/hyperledger/aries-framework-go/pkg/didcomm/transport"
	"github.com/hyperledger/aries-framework-go/pkg/doc/cm"
	"github.com/hyperledger/aries-framework-go/pkg/doc/presexch"
	"github.com/hyperledger/aries-framework-go/pkg/doc/verifiable"
	vdrapi "github.com/hyperledger/aries-framework-go/pkg/framework/aries/api/vdr"
	kmsapi "github.com/hyperledger/aries-framework-go/pkg/kms"
	mockkms "github.com/hyperledger/aries-framework-go/pkg/mock/kms"
	vdrpkg "github.com/hyperledger/aries-framework-go/pkg/vdr"
	"github.com/hyperledger/aries-framework-go/pkg/vdr/web"
	arieslog "github.com/hyperledger/aries-framework-go/spi/log"
	"github.com/hyperledger/aries-framework-go/spi/storage"
)

var pdBytes []byte

const (
	// issuer html templates
	issuerHTML          = "./templates/issuer/issuer.html"
	waciIssuerHTML      = "./templates/issuer/waci-issuer.html"
	oidcIssuerHTML      = "./templates/issuer/oidc-issuer.html"
	oidcIssuerLoginHTML = "./templates/issuer/oidc-login.html"
	openid4vcIssuerHTML = "./templates/issuer/openid4vc-issuer.html"

	// verifier html templates
	verifierHTML          = "./templates/verifier/verifier.html"
	waciVerifierHTML      = "./templates/verifier/waci-verifier.html"
	oidcVerifierHTML      = "./templates/verifier/oidc-verifier.html"
	openid4vcVerifierHTML = "./templates/verifier/openid4vc-verifier.html"

	// CHAPI html templates
	webWalletHTML = "./templates/webWallet.html"
)

// Mock signer for signing VCs.
const (
	didKey   = "did:key:z6MknC1wwS6DEYwtGbZZo2QvjQjkh2qSBjb4GYmbye8dv4S5"
	pkBase58 = "2MP5gWCnf67jvW3E4Lz8PpVrDWAXMYY1sDxjnkEnKhkkbKD7yP2mkVeyVpu5nAtr3TeDgMNjBPirk2XcQacs3dvZ"
	kid      = "did:key:z6MknC1wwS6DEYwtGbZZo2QvjQjkh2qSBjb4GYmbye8dv4S5#z6MknC1wwS6DEYwtGbZZo2QvjQjkh2qSBjb4GYmbye8dv4S5"
	secret   = "mock-issuer-pin-secret"
)

var logger = log.New("mock-adapter")

type issuerConfiguration struct {
	Issuer                string          `json:"issuer"`
	AuthorizationEndpoint string          `json:"authorization_endpoint"`
	CredentialEndpoint    string          `json:"credential_endpoint"`
	TokenEndpoint         string          `json:"token_endpoint"`
	CredentialManifests   json.RawMessage `json:"credential_manifests"`
}

type openid4vcIssuerConfiguration struct {
	CredentialEndpoint   string          `json:"credential_endpoint"`
	CredentialsSupported json.RawMessage `json:"credentials_supported"`
	CredentialIssuer     json.RawMessage `json:"credential_issuer,omitempty"`
	Pin                  int             `json:"pin,omitempty"`
	TokenEndpoint        string          `json:"token_endpoint"`
}

type openid4ciDemo struct {
	InitiateUrl string
	Pin         int
}

// waciIssuanceData contains state of WACI demo.
type waciIssuanceData struct {
	CredentialManifest json.RawMessage `json:"credential_manifest"`
	CredentialResponse json.RawMessage `json:"credential_response"`
	Credential         json.RawMessage `json:"credential"`
}

type adapterApp struct {
	agent  *didComm
	store  storage.Store
	kms    kmsapi.KeyManager
	vdr    vdrapi.Registry
	crypto cryptoapi.Crypto
}

type vpToken struct {
	PresentationDefinition presexch.PresentationDefinition `json:"presentation_definition"`
}

type claims struct {
	VPToken vpToken `json:"vp_token"`
}

type openid4vcShareRequestHeader struct {
	Kid string `json:"kid"`
}

type openid4vcShareRequestPayload struct {
	IssuedAt     int64  `json:"iat"`
	ResponseType string `json:"response_type"`
	Scope        string `json:"scope,omitempty"`
	Nonce        string `json:"nonce"`
	ClientId     string `json:"client_id"`
	RedirectURI  string `json:"redirect_uri"`
	State        string `json:"state,omitempty"`
	Expiry       int64  `json:"exp"`
	Claims       claims `json:"claims"`
}

type openid4vcIssuerStore struct {
	Pin int `json:"pin"`
}

func startAdapterApp(agent *didComm, router *mux.Router) error {
	log.SetLevel("", arieslog.DEBUG)

	prov := mem.NewProvider()

	store, err := prov.OpenStore("verifier")
	if err != nil {
		return fmt.Errorf("failed to create store : %w", err)
	}

	kmsProv, err := mockkms.NewProviderForKMS(prov, &noop.NoLock{})
	if err != nil {
		return fmt.Errorf("failed to create kms provider : %w", err)
	}

	keyManager, err := localkms.New("local-lock://test/master/key/", kmsProv)
	if err != nil {
		return fmt.Errorf("failed to create key manager : %w", err)
	}

	edPriv := ed25519.PrivateKey(base58.Decode(pkBase58))
	if len(edPriv) == 0 {
		return fmt.Errorf("error converting bad public key")
	}

	_, _, err = keyManager.ImportPrivateKey(edPriv, kmsapi.ED25519Type, kmsapi.WithKeyID("dfiDkP85Xq5Cald-Q7nT4515MNAcguRJzJpD4CsepAg"))

	crypto, err := tinkcrypto.New()
	if err != nil {
		return fmt.Errorf("failed to create crypto : %w", err)
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	vdr := vdrpkg.New(vdrpkg.WithVDR(key.New()), vdrpkg.WithVDR(&webVDR{
		http: &http.Client{Transport: tr},
		VDR:  web.New(),
	}))

	app := adapterApp{agent: agent, store: store, kms: keyManager, crypto: crypto, vdr: vdr}

	actionCh := make(chan service.DIDCommAction)

	err = agent.DIDExchClient.RegisterActionEvent(actionCh)
	if err != nil {
		return fmt.Errorf("failed to register action events on didexchange-client : %w", err)
	}

	err = agent.PresentProofClient.RegisterActionEvent(actionCh)
	if err != nil {
		return fmt.Errorf("failed to register action events on present-proof-client : %w", err)
	}

	err = agent.IssueCredentialClient.RegisterActionEvent(actionCh)
	if err != nil {
		return fmt.Errorf("failed to register action events on issue-credential-client : %w", err)
	}

	go listenForDIDCommMsg(actionCh, store)

	// issuer routes
	router.HandleFunc("/issuer", app.issuer)
	router.HandleFunc("/issuer/waci", app.waciIssuer)
	router.HandleFunc("/issuer/waci-issuance", app.waciIssuance)
	router.HandleFunc("/issuer/waci-issuance-v2", app.waciIssuanceV2)
	router.HandleFunc("/issuer/waci-issuance/{id}", app.waciIssuanceCallback)
	router.HandleFunc("/issuer/oidc", app.oidcIssuer)
	router.HandleFunc("/issuer/oidc/login", app.oidcIssuerLogin)
	router.HandleFunc("/issuer/oidc/issuance", app.initiateIssuance).Methods(http.MethodPost)
	router.HandleFunc("/{id}/.well-known/openid-configuration", app.wellKnownConfiguration).Methods(http.MethodGet)
	router.HandleFunc("/{id}/issuer/oidc/authorize", app.issuerAuthorize).Methods(http.MethodGet)
	router.HandleFunc("/issuer/oidc/authorize-request", app.issuerSendAuthorizeResponse).Methods(http.MethodPost)
	router.HandleFunc("/{id}/issuer/oidc/token", app.issuerTokenEndpoint).Methods(http.MethodPost)
	router.HandleFunc("/{id}/issuer/oidc/credential", app.issuerCredentialEndpoint).Methods(http.MethodPost)
	router.HandleFunc("/issuer/openid4vc", app.openid4vcIssuer)
	router.HandleFunc("/issuer/openid4vc/issuance", app.openid4vcInitiatePreAuthorizedIssuance).Methods(http.MethodPost)
	router.HandleFunc("/{id}/issuer/openid4vc/token", app.openid4vcIssuerTokenEndpoint).Methods(http.MethodPost)
	router.HandleFunc("/{id}/issuer/openid4vc/credential", app.openid4vcIssuerCredentialEndpoint).Methods(http.MethodPost)

	// verifier routes
	router.HandleFunc("/verifier", app.verifier)
	router.HandleFunc("/verifier/waci", app.waciVerifier)
	router.HandleFunc("/verifier/waci-share", app.waciShare)
	router.HandleFunc("/verifier/waci-share-v2", app.waciShareV2)
	router.HandleFunc("/verifier/waci-share/{id}", app.waciShareCallback)
	router.HandleFunc("/verifier/oidc", app.oidcVerifier)
	router.HandleFunc("/verifier/oidc/share", app.oidcShare)
	router.HandleFunc("/verifier/oidc/share/cb", app.oidcShareCallback)
	router.HandleFunc("/verifier/openid4vc", app.openid4vcVerifier)
	router.HandleFunc("/verifier/openid4vc/share", app.openid4vcShare)
	router.HandleFunc("/verifier/openid4vc/share/cb", app.openid4vcShareCallback)

	// CHAPI flow routes
	router.HandleFunc("/web-wallet", app.webWallet)

	return nil
}

// issuer html template endpoints
func (v *adapterApp) issuer(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, issuerHTML, nil)
}

func (v *adapterApp) waciIssuer(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, waciIssuerHTML, nil)
}

func (v *adapterApp) oidcIssuer(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, oidcIssuerHTML, nil)
}

func (v *adapterApp) openid4vcIssuer(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, openid4vcIssuerHTML, nil)
}

func (v *adapterApp) oidcIssuerLogin(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, oidcIssuerLoginHTML, nil)
}

// verifier html template endpoints
func (v *adapterApp) verifier(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, verifierHTML, nil)
}

func (v *adapterApp) waciVerifier(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, waciVerifierHTML, nil)
}

func (v *adapterApp) oidcVerifier(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, oidcVerifierHTML, nil)
}

func (v *adapterApp) openid4vcVerifier(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, openid4vcVerifierHTML, nil)
}

// chapi html template endpoints
func (v *adapterApp) webWallet(w http.ResponseWriter, r *http.Request) {
	loadTemplate(w, webWalletHTML, nil)
}

// data endpoints
func (v *adapterApp) waciShare(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// generate OOB invitation
	inv, err := v.agent.OOBClient.CreateInvitation(nil,
		outofband.WithAccept(transport.MediaTypeAIP2RFC0019Profile, transport.MediaTypeProfileDIDCommAIP1),
		outofband.WithGoal("share-vp", "streamlined-vp"))
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create oob invitation : %s", err))

		return
	}

	pdBytes = []byte(r.FormValue("pEx"))

	v.waciInvitationRedirect(w, r, inv)
}

func (v *adapterApp) waciShareV2(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// generate OOB V2 invitation
	inv, err := v.agent.OOBV2Client.CreateInvitation(
		outofbandv2.WithAccept(transport.MediaTypeDIDCommV2Profile, transport.MediaTypeAIP2RFC0587Profile),
		outofbandv2.WithFrom(v.agent.OrbDIDV2), outofbandv2.WithGoal("share-vp", "streamlined-vp"))
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create oob invitation : %s", err))

		return
	}

	pdBytes = []byte(r.FormValue("pEx"))

	v.waciInvitationRedirect(w, r, inv)
}

func (v *adapterApp) waciIssuance(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// generate OOB invitation
	inv, err := v.agent.OOBClient.CreateInvitation(nil,
		outofband.WithGoal("issue-vc", "streamlined-vc"),
		outofband.WithAccept(transport.MediaTypeAIP2RFC0019Profile, transport.MediaTypeProfileDIDCommAIP1))
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create oob invitation : %s", err))

		return
	}

	v.persistWACIIssuanceData(w, r, inv.ID)
	v.waciInvitationRedirect(w, r, inv)
}

func (v *adapterApp) waciIssuanceV2(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// generate OOB V2 invitation
	inv, err := v.agent.OOBV2Client.CreateInvitation(outofbandv2.WithAccept(transport.MediaTypeDIDCommV2Profile),
		outofbandv2.WithFrom(v.agent.OrbDIDV2), outofbandv2.WithGoal("issue-vc", "streamlined-vc"))
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create oob invitation : %s", err))

		return
	}

	v.persistWACIIssuanceData(w, r, inv.ID)
	v.waciInvitationRedirect(w, r, inv)
}

func (v *adapterApp) waciInvitationRedirect(w http.ResponseWriter, r *http.Request, inv interface{}) {
	r.ParseForm()

	invBytes, err := json.Marshal(inv)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal invitation : %s", err))

		return
	}

	redirectURL := fmt.Sprintf("%s/waci?oob=%s", r.FormValue("walletURL"),
		base64.URLEncoding.EncodeToString(invBytes))

	logger.Infof("waci redirect : url=%s oob-invitation=%s", redirectURL, string(invBytes))

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (v *adapterApp) persistWACIIssuanceData(w http.ResponseWriter, r *http.Request, invID string) {
	waciData, err := json.Marshal(&waciIssuanceData{
		CredentialResponse: []byte(r.FormValue("response")),
		CredentialManifest: []byte(r.FormValue("credManifest")),
		Credential:         []byte(r.FormValue("credToIssue")),
	})
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to persist waci data : %s", err))

		return
	}

	err = v.store.Put(getWACIIssuanceDataStoreKeyPrefix(invID), waciData)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to persist waci data : %s", err))

		return
	}

	logger.Infof("waci redirect :data=%s invitationID=%s", string(waciData), invID)
}

func (v *adapterApp) waciShareCallback(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	_, err := v.store.Get(id)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get interaction data : %s", err))

		return
	}

	loadTemplate(w, waciVerifierHTML, map[string]interface{}{"Msg": "Successfully Received Presentation"})
}

func (v *adapterApp) waciIssuanceCallback(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	_, err := v.store.Get(id)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get interaction data : %s", err))

		return
	}

	loadTemplate(w, waciIssuerHTML, map[string]interface{}{"Msg": "Successfully Sent Credential to holder"})
}

func (v *adapterApp) oidcShare(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	walletAuthURL := r.FormValue("walletAuthURL")
	pdBytes = []byte(r.FormValue("pEx"))

	var pd *presexch.PresentationDefinition
	err := json.Unmarshal(pdBytes, &pd)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to unmarshal presentation definition : %s", err))

		return
	}

	authClaims := &OIDCAuthClaims{
		VPToken: &VPToken{
			PresDef: pd,
		},
	}

	claimsBytes, err := json.Marshal(authClaims)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to unmarshal invitation : %s", err))

		return
	}

	state := uuid.NewString()

	// TODO: use OIDC client library
	// construct wallet auth req with PEx
	req, err := http.NewRequest("GET", walletAuthURL, nil)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get interaction data : %s", err))

		return
	}

	q := req.URL.Query()
	q.Add("client_id", "demo-verifier")
	q.Add("redirect_uri", os.Getenv(demoExternalURLEnvKey)+"/verifier/oidc/share/cb")
	q.Add("scope", "openid")
	q.Add("state", state)
	q.Add("claims", string(claimsBytes))

	req.URL.RawQuery = q.Encode()

	redirectURL := req.URL.String()

	logger.Infof("oidc share redirect : url=%s claims=%s", redirectURL, string(claimsBytes))

	err = v.store.Put(state, pdBytes)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to save state data : %s", err))

		return
	}

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (v *adapterApp) oidcShareCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")

	pdBytes, err := v.store.Get(state)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get oidc state data : %s", err))

		return
	}

	var pd *presexch.PresentationDefinition

	err = json.Unmarshal(pdBytes, &pd)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to unmarshal presentation definition : %s", err))

		return
	}

	idToken := r.URL.Query().Get("id_token")
	vpToken := r.URL.Query().Get("vp_token")

	logger.Infof("oidc share callback : id_token=%s vp_token=%s",
		idToken, vpToken)

	var claims *OIDCTokenClaims

	token, err := jwt.ParseSigned(idToken)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to parsed token : %s", err))

		return
	}

	err = token.UnsafeClaimsWithoutVerification(&claims)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to convert to claim object : %s", err))

		return
	}

	presSubBytes, err := json.Marshal(claims.VPToken)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal _vp_token : %s", err))

		return
	}

	logger.Infof("oidc share callback : _vp_token=%v vp_token=%s", string(presSubBytes), vpToken)

	pres, err := verifiable.ParsePresentation([]byte(vpToken), verifiable.WithPresJSONLDDocumentLoader(ld.NewDefaultDocumentLoader(nil)), verifiable.WithPresDisabledProofCheck())
	if err != nil {
		loadTemplate(w, oidcVerifierHTML,
			map[string]interface{}{
				"ErrMsg":                    fmt.Sprintf("ERROR: failed to validate presentation : %s", err),
				"ID_TOKEN":                  "\n" + idToken,
				"DECODED_VPDEF_IN_ID_TOKEN": string(presSubBytes),
				"VP_TOKEN":                  string(vpToken),
			},
		)

		return
	}

	pres.JWT = ""

	presBytes, _ := pres.MarshalJSON()

	loadTemplate(w, oidcVerifierHTML,
		map[string]interface{}{
			"Msg":                       "Successfully Received Presentation",
			"ID_TOKEN":                  "\n" + idToken,
			"DECODED_VPDEF_IN_ID_TOKEN": string(presSubBytes),
			"VP_TOKEN":                  string(vpToken),
			"DECODED_VP_TOKEN":          string(presBytes),
		},
	)
}

func (v *adapterApp) openid4vcShare(w http.ResponseWriter, r *http.Request) {
	pdBytes := []byte(`
		{
			"id": "22c77155-edf2-4ec5-8d44-b393b4e4fa38",
			"input_descriptors": [
				{
					"id": "20b073bb-cede-4912-9e9d-334e5702077b",
					"schema": [
						{
							"uri": "https://www.w3.org/2018/credentials#VerifiableCredential"
						}
					],
					"constraints": {
						"fields": [
							{
								"path": [
									"$.credentialSubject.familyName"
								]
							}
						]
					}
				}
			]
		}
	`)
	var pd *presexch.PresentationDefinition
	json.Unmarshal(pdBytes, &pd)

	claims := claims{VPToken: vpToken{PresentationDefinition: *pd}}

	requestObjectPayload, err := json.Marshal(&openid4vcShareRequestPayload{
		IssuedAt:     time.Now().Unix(),
		ResponseType: "id_token",
		Scope:        "openid",
		Nonce:        uuid.NewString(),
		ClientId:     "did:ion:EiAv0eJ5cB0hGWVH5YbY-uw1K71EpOST6ztueEQzVCEc0A:eyJkZWx0YSI6eyJwYXRjaGVzIjpbeyJhY3Rpb24iOiJyZXBsYWNlIiwiZG9jdW1lbnQiOnsicHVibGljS2V5cyI6W3siaWQiOiJzaWdfY2FiNjVhYTAiLCJwdWJsaWNLZXlKd2siOnsiY3J2Ijoic2VjcDI1NmsxIiwia3R5IjoiRUMiLCJ4IjoiOG15MHFKUGt6OVNRRTkyRTlmRFg4ZjJ4bTR2X29ZMXdNTEpWWlQ1SzhRdyIsInkiOiIxb0xsVG5rNzM2RTNHOUNNUTh3WjJQSlVBM0phVnY5VzFaVGVGSmJRWTFFIn0sInB1cnBvc2VzIjpbImF1dGhlbnRpY2F0aW9uIiwiYXNzZXJ0aW9uTWV0aG9kIl0sInR5cGUiOiJFY2RzYVNlY3AyNTZrMVZlcmlmaWNhdGlvbktleTIwMTkifV0sInNlcnZpY2VzIjpbeyJpZCI6ImxpbmtlZGRvbWFpbnMiLCJzZXJ2aWNlRW5kcG9pbnQiOnsib3JpZ2lucyI6WyJodHRwczovL3N3ZWVwc3Rha2VzLmRpZC5taWNyb3NvZnQuY29tLyJdfSwidHlwZSI6IkxpbmtlZERvbWFpbnMifV19fV0sInVwZGF0ZUNvbW1pdG1lbnQiOiJFaUFwcmVTNy1Eczh5MDFnUzk2cE5iVnpoRmYxUlpvblZ3UkswbG9mZHdOZ2FBIn0sInN1ZmZpeERhdGEiOnsiZGVsdGFIYXNoIjoiRWlEMWRFdUVldERnMnhiVEs0UDZVTTNuWENKVnFMRE11M29IVWNMamtZMWFTdyIsInJlY292ZXJ5Q29tbWl0bWVudCI6IkVpREFkSzFWNkpja1BpY0RBcGFxV2IyZE95MFRNcmJKTmllNmlKVzk4Zk54bkEifX0",
		RedirectURI:  os.Getenv(demoExternalURLEnvKey) + "/verifier/openid4vc/share/cb",
		Expiry:       time.Now().Unix() + 60*10,
		Claims:       claims,
	})
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to construct OpenID4VC Share Request Object : %s", err))

		return
	}

	requestClaims := map[string]interface{}{}
	err = json.Unmarshal(requestObjectPayload, &requestClaims)
	if err != nil {
		handleError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal request claims : %s", err))
		return
	}

	requestObjectHeaders, err := json.Marshal(openid4vcShareRequestHeader{Kid: strings.Split(kid, "#")[0]})
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal request object header bytes : %s", err))
		return
	}

	requestHeaders := map[string]interface{}{}
	err = json.Unmarshal(requestObjectHeaders, &requestHeaders)
	if err != nil {
		handleError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal request headers : %s", err))
		return
	}

	result, err := didsignjwt.SignJWT(requestHeaders, requestClaims, kid, didsignjwt.UseDefaultSigner(v.kms, v.crypto), v.vdr)
	if err != nil {
		handleError(w, http.StatusInternalServerError, fmt.Sprintf("failed to sign request object : %s", err))
		return
	}

	w.Header().Set("Content-Type", "application/jwt")
	w.WriteHeader(200)
	w.Write([]byte(result))
}

func (v *adapterApp) openid4vcShareCallback(w http.ResponseWriter, r *http.Request) {
	idToken := r.FormValue("id_token")
	if len(idToken) == 0 {
		handleError(w, http.StatusInternalServerError, fmt.Sprintf("failed to verify presentation: id_token is empty"))
		return
	}

	err := didsignjwt.VerifyJWT(idToken, v.vdr)
	if err != nil {
		handleError(w, http.StatusInternalServerError, fmt.Sprintf("failed to verify signature on id_token: %s", err))
		return
	}

	vpToken := r.FormValue("vp_token")
	if len(vpToken) == 0 {
		handleError(w, http.StatusInternalServerError, fmt.Sprintf("failed to verify presentation: vp_token is empty"))
		return
	}

	err = didsignjwt.VerifyJWT(vpToken, v.vdr)
	if err != nil {
		handleError(w, http.StatusInternalServerError, fmt.Sprintf("failed to verify signature on vp_token: %s", err))
		return
	}

	logger.Infof("oidc share callback: id_token=%s", idToken)
	logger.Infof("oidc share callback: vp_token=%s", vpToken)

	// TODO add validation

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	return
}

func (v *adapterApp) initiateIssuance(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	walletURL := r.FormValue("walletInitIssuanceURL")
	credentialTypes := strings.Split(r.FormValue("credentialTypes"), ",")
	manifestIDs := strings.Split(r.FormValue("manifestIDs"), ",")
	issuerURL := r.FormValue("issuerURL")
	credManifest := r.FormValue("credManifest")
	credentials := r.FormValue("credsToIssue")

	key := uuid.NewString()
	issuer := issuerURL + "/" + key
	issuerConf, err := json.MarshalIndent(&issuerConfiguration{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + "/issuer/oidc/authorize",
		TokenEndpoint:         issuer + "/issuer/oidc/token",
		CredentialEndpoint:    issuer + "/issuer/oidc/credential",
		CredentialManifests:   []byte(credManifest),
	}, "", "	")
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to prepare issuer wellknown configuration : %s", err))

		return
	}

	err = v.store.Put(key, issuerConf)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to prepare server configuration : %s", err))

		return
	}

	var credentialsToSave map[string]json.RawMessage
	err = json.Unmarshal([]byte(credentials), &credentialsToSave)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to parse credentials : %s", err))

		return
	}

	for ct, credential := range credentialsToSave {
		err = v.store.Put(getCredStoreKeyPrefix(key, ct), credential)
		if err != nil {
			handleError(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to server configuration : %s", err))

			return
		}
	}

	u, err := url.Parse(walletURL)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to parse wallet init issuance URL : %s", err))

		return
	}

	q := u.Query()
	q.Set("issuer", issuer)

	for _, credType := range credentialTypes {
		q.Add("credential_type", credType)
	}

	for _, manifestID := range manifestIDs {
		q.Add("manifest_id", manifestID)
	}

	u.RawQuery = q.Encode()

	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (v *adapterApp) wellKnownConfiguration(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	issuerConf, err := v.store.Get(id)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to read wellknown configuration : %s", err))

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(issuerConf)
}

func (v *adapterApp) issuerAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		handleError(w, http.StatusBadRequest,
			fmt.Sprintf("failed to parse request : %s", err))

		return
	}

	claims, err := url.PathUnescape(r.Form.Get("claims"))
	if err != nil {
		handleError(w, http.StatusBadRequest,
			fmt.Sprintf("failed to read claims : %s", err))

		return
	}

	redirectURI, err := url.PathUnescape(r.Form.Get("redirect_uri"))
	if err != nil {
		handleError(w, http.StatusBadRequest,
			fmt.Sprintf("failed to read redirect URI : %s", err))

		return
	}

	scope := r.Form.Get("scope")
	state := r.Form.Get("state")
	responseType := r.Form.Get("response_type")
	clientID := r.Form.Get("client_id")

	// basic validation only.
	if claims == "" || redirectURI == "" || clientID == "" || state == "" {
		handleError(w, http.StatusBadRequest, fmt.Sprintf("Invalid Request"))

		return
	}

	authState := uuid.NewString()

	authRequest, err := json.Marshal(map[string]string{
		"claims":        claims,
		"scope":         scope,
		"state":         state,
		"response_type": responseType,
		"client_id":     clientID,
		"redirect_uri":  redirectURI,
	})
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to process authorization request : %s", err))

		return
	}

	err = v.store.Put(getAuthStateKeyPrefix(authState), authRequest)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to save state : %s", err))

		return
	}

	authStateCookie := http.Cookie{
		Name:    "state",
		Value:   authState,
		Expires: time.Now().Add(5 * time.Minute),
		Path:    "/",
	}

	http.SetCookie(w, &authStateCookie)
	http.Redirect(w, r, "/issuer/oidc/login", http.StatusFound)
}

func (v *adapterApp) issuerSendAuthorizeResponse(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("state")
	if err != nil {
		handleError(w, http.StatusForbidden, "invalid state")

		return
	}

	authRqstBytes, err := v.store.Get(getAuthStateKeyPrefix(stateCookie.Value))
	if err != nil {
		handleError(w, http.StatusBadRequest, "invalid request")

		return
	}

	var authRequest map[string]string
	err = json.Unmarshal(authRqstBytes, &authRequest)
	if err != nil {
		handleError(w, http.StatusInternalServerError, "failed to read request")

		return
	}

	redirectURI, ok := authRequest["redirect_uri"]
	if !ok {
		handleError(w, http.StatusInternalServerError, "failed to redirect, invalid URL")

		return
	}

	state, ok := authRequest["state"]
	if !ok {
		handleError(w, http.StatusInternalServerError, "failed to redirect, invalid state")

		return
	}

	authCode := uuid.NewString()
	v.store.Put(getAuthCodeKeyPrefix(authCode), []byte(stateCookie.Value))

	redirectTo := fmt.Sprintf("%s?code=%s&state=%s", redirectURI, authCode, state)

	// TODO process credential types or manifests from claims and prepare credential endpoint with credential to be issued.
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func (v *adapterApp) issuerTokenEndpoint(w http.ResponseWriter, r *http.Request) {
	setOIDCResponseHeaders(w)

	code := r.FormValue("code")
	redirectURI := r.FormValue("redirect_uri")
	grantType := r.FormValue("grant_type")

	if grantType != "authorization_code" {
		sendOIDCErrorResponse(w, "unsupported grant type", http.StatusBadRequest)
		return
	}

	authState, err := v.store.Get(getAuthCodeKeyPrefix(code))
	if err != nil {
		sendOIDCErrorResponse(w, "invalid state", http.StatusBadRequest)
		return
	}

	authRqstBytes, err := v.store.Get(getAuthStateKeyPrefix(string(authState)))
	if err != nil {
		sendOIDCErrorResponse(w, "invalid request", http.StatusBadRequest)
		return
	}

	var authRequest map[string]string
	err = json.Unmarshal(authRqstBytes, &authRequest)
	if err != nil {
		sendOIDCErrorResponse(w, "failed to read request", http.StatusInternalServerError)
		return
	}

	if authRedirectURI := authRequest["redirect_uri"]; authRedirectURI != redirectURI {
		sendOIDCErrorResponse(w, "request validation failed", http.StatusInternalServerError)
		return
	}

	mockAccessToken := uuid.NewString()
	mockIssuerID := mux.Vars(r)["id"]

	err = v.store.Put(getAccessTokenKeyPrefix(mockAccessToken), []byte(mockIssuerID))
	if err != nil {
		sendOIDCErrorResponse(w, "failed to save token state", http.StatusInternalServerError)
		return
	}

	response, err := json.Marshal(map[string]interface{}{
		"token_type":   "Bearer",
		"access_token": mockAccessToken,
		"expires_in":   3600 * time.Second,
	})
	// TODO add id_token, c_nonce, c_nonce_expires_in
	if err != nil {
		sendOIDCErrorResponse(w, "response_write_error", http.StatusBadRequest)

		return
	}

	w.Write(response)
}

func (v *adapterApp) issuerCredentialEndpoint(w http.ResponseWriter, r *http.Request) {
	setOIDCResponseHeaders(w)

	format := r.FormValue("format")
	credentialType := r.FormValue("type")

	if format != "" && format != "ldp_vc" && format != "jwt_vc" {
		sendOIDCErrorResponse(w, "unsupported format requested", http.StatusBadRequest)
		return
	}

	authHeader := strings.Split(r.Header.Get("Authorization"), "Bearer ")
	if len(authHeader) != 2 {
		sendOIDCErrorResponse(w, "malformed token", http.StatusBadRequest)
		return
	}

	if authHeader[1] == "" {
		sendOIDCErrorResponse(w, "invalid token", http.StatusForbidden)
		return
	}

	mockIssuerID := mux.Vars(r)["id"]

	issuerID, err := v.store.Get(getAccessTokenKeyPrefix(authHeader[1]))
	if err != nil {
		sendOIDCErrorResponse(w, "unsupported format requested", http.StatusBadRequest)
		return
	}

	if mockIssuerID != string(issuerID) {
		sendOIDCErrorResponse(w, "invalid transaction", http.StatusForbidden)
		return
	}

	credentialBytes, err := v.store.Get(getCredStoreKeyPrefix(mockIssuerID, credentialType))
	if err != nil {
		sendOIDCErrorResponse(w, "failed to get credential", http.StatusInternalServerError)
		return
	}

	docLoader := ld.NewDefaultDocumentLoader(nil)
	credential, err := verifiable.ParseCredential(credentialBytes, verifiable.WithJSONLDDocumentLoader(docLoader))
	if err != nil {
		sendOIDCErrorResponse(w, "failed to prepare credential", http.StatusInternalServerError)
		return
	}

	var credBytes []byte

	switch format {
	case "", "ldp", "ldp_vc":
		err = signCredentialWithED25519(credential)
		if err != nil {
			sendOIDCErrorResponse(w, "failed to issue credential", http.StatusInternalServerError)
			return
		}

		credBytes, err = credential.MarshalJSON()
		if err != nil {
			sendOIDCErrorResponse(w, "failed to write credential bytes", http.StatusInternalServerError)
			return
		}
	case "jwt", "jwt_vc":
		claims, err := credential.JWTClaims(false)
		if err != nil {
			sendOIDCErrorResponse(w, "failed to create credential claims", http.StatusInternalServerError)
			return
		}

		jws, err := signJWTCredentialWithED25519(claims)
		if err != nil {
			sendOIDCErrorResponse(w, "failed to issue JWT credential", http.StatusInternalServerError)
			return
		}

		credBytes = []byte("\"" + jws + "\"")
	}

	response, err := json.Marshal(map[string]interface{}{
		"format":     format,
		"credential": json.RawMessage(credBytes),
	})
	// TODO add support for acceptance token & nonce for deferred flow.
	if err != nil {
		sendOIDCErrorResponse(w, "response_write_error", http.StatusBadRequest)
		return
	}

	w.Write(response)
}

func (v *adapterApp) openid4vcInitiatePreAuthorizedIssuance(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	issuerURL := r.FormValue("issuerURL")
	credentialsSupported := r.FormValue("credentialsSupported")

	var data map[string]map[string]interface{}
	var credType string

	if err := json.Unmarshal([]byte(credentialsSupported), &data); err != nil {
		panic(err)
	}

	for _, u := range data {
		credType = fmt.Sprintf("%v", u["types"].([]interface{})[1])
	}

	pinNumber, err := generateRandomNumber(6)

	issuerData, err := json.MarshalIndent(&openid4vcIssuerStore{
		Pin: pinNumber,
	}, "", "	")
	err = v.store.Put(secret, issuerData)

	key := uuid.NewString()
	issuer := issuerURL + "/" + key
	issuerConf, err := json.MarshalIndent(&openid4vcIssuerConfiguration{
		CredentialsSupported: []byte(credentialsSupported),
		CredentialEndpoint:   issuer + "/issuer/openid4vc/credential",
		TokenEndpoint:        issuer + "/issuer/openid4vc/token",
	}, "", "	")
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to prepare issuer wellknown configuration : %s", err))
		return
	}

	t, err := template.ParseFiles(openid4vcIssuerHTML)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to parse issuer html template : %s", err))
		return
	}

	if err != nil {
		logger.Errorf(fmt.Sprintf("execute html template: %s", err.Error()))
	}

	err = v.store.Put(key, issuerConf)
	if err != nil {
		handleError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to prepare server configuration : %s", err))

		return
	}

	initiateUrl := "openid-initiate-issuance://?issuer=" + url.QueryEscape(issuer) + "&credential_type=" +
		url.QueryEscape(credType) + "&pre-authorized_code=" + url.QueryEscape(uuid.NewString()) + "&user_pin_required=true"

	err = t.Execute(w, openid4ciDemo{
		InitiateUrl: initiateUrl,
		Pin:         pinNumber,
	})
}

func listenForDIDCommMsg(actionCh chan service.DIDCommAction, store storage.Store) {
	for action := range actionCh {
		logger.Infof("received action message : type=%s", action.Message.Type())

		switch action.Message.Type() {
		case didexchange.RequestMsgType:
			action.Continue(nil)
		case presentproofsvc.ProposePresentationMsgTypeV2, presentproofsvc.ProposePresentationMsgTypeV3:
			thID, err := action.Message.ThreadID()
			if err != nil {
				logger.Errorf("failed to get thread ID", err)
				action.Stop(nil)
			}

			pd := presexch.PresentationDefinition{}
			err = json.Unmarshal(pdBytes, &pd)
			if err != nil {
				logger.Errorf("failed to unmarshal presentation definition", err)
				action.Stop(nil)
			}

			err = store.Put(thID, pdBytes)
			if err != nil {
				logger.Errorf("failed to save presentation definition", err)
				action.Stop(nil)
			}

			continueArg := presentproof.WithRequestPresentation(&presentproof.RequestPresentation{
				Comment: "Request Presentation",
				Attachments: []decorator.GenericAttachment{
					{
						ID:        uuid.NewString(),
						MediaType: "application/json",
						Data: decorator.AttachmentData{
							JSON: struct {
								Challenge string                           `json:"challenge"`
								Domain    string                           `json:"domain"`
								PD        *presexch.PresentationDefinition `json:"presentation_definition"`
							}{
								Challenge: uuid.NewString(),
								Domain:    uuid.NewString(),
								PD:        &pd,
							},
						},
					},
				},
				WillConfirm: true,
			})

			action.Continue(continueArg)
		case presentproofsvc.PresentationMsgTypeV2, presentproofsvc.PresentationMsgTypeV3:
			thID, err := action.Message.ThreadID()
			if err != nil {
				logger.Errorf("failed to get thread ID", err)
				action.Stop(nil)
			}

			action.Continue(presentproofsvc.WithProperties(
				map[string]interface{}{
					"~web-redirect": &decorator.WebRedirect{
						Status: "OK",
						URL:    os.Getenv(demoExternalURLEnvKey) + "/verifier/waci-share/" + thID,
					},
				},
			))
		case issuecredential.ProposeCredentialMsgTypeV2, issuecredential.ProposeCredentialMsgTypeV3:
			thID, err := action.Message.ThreadID()
			if err != nil {
				logger.Errorf("failed to get thread ID", err)
				action.Stop(nil)
			}

			err = store.Put(thID, []byte(thID))
			if err != nil {
				logger.Errorf("failed to save interaction data", err)
				action.Stop(nil)
			}

			var msgData map[string]interface{}
			err = action.Message.Decode(&msgData)
			if err != nil {
				logger.Errorf("failed to decode propose credential message", err)
				action.Stop(nil)
			}

			var invitationID string
			if invID, ok := msgData["invitationID"]; ok {
				invitationID, _ = invID.(string)
			} else if invID, ok := msgData["pthid"]; ok {
				invitationID, _ = invID.(string)
			}

			waciData, err := readWACIIssuanceData(store, invitationID, action.Message.ID())
			if err != nil {
				logger.Errorf("failed to get WACI issuance data", err)
				action.Stop(nil)
			}

			vp, err := createResponseVP(waciData.CredentialResponse, waciData.Credential, false)
			if err != nil {
				logger.Errorf("failed to prepare response", err)
				action.Stop(nil)
			}

			credResponseBytes, err := vp.MarshalJSON()
			if err != nil {
				logger.Errorf("failed to prepare response bytes", err)
				action.Stop(nil)
			}

			offerCredMsg, err := createOfferCredentialMsg(waciData.CredentialManifest, credResponseBytes)
			if err != nil {
				logger.Errorf("failed to prepare offer credential message", err)
				action.Stop(nil)
			}

			action.Continue(issuecredential.WithOfferCredential(offerCredMsg))
		case issuecredential.RequestCredentialMsgTypeV2, issuecredential.RequestCredentialMsgTypeV3:
			thID, err := action.Message.ThreadID()
			if err != nil {
				logger.Errorf("failed to get thread ID", err)
				action.Stop(nil)
			}

			waciData, err := readWACIIssuanceData(store, thID, "")
			if err != nil {
				logger.Errorf("failed to get WACI issuance data", err)
				action.Stop(nil)
			}

			vp, err := createResponseVP(waciData.CredentialResponse, waciData.Credential, true)
			if err != nil {
				logger.Errorf("failed to prepare response", err)
				action.Stop(nil)
			}

			credResponseBytes, err := vp.MarshalJSON()
			if err != nil {
				logger.Errorf("failed to prepare response bytes", err)
				action.Stop(nil)
			}

			issueCredMsg, err := createIssueCredentialMsg(credResponseBytes, os.Getenv(demoExternalURLEnvKey)+"/issuer/waci-issuance/"+thID)
			if err != nil {
				logger.Errorf("failed to prepare issue credential message", err)
				action.Stop(nil)
			}

			action.Continue(issuecredential.WithIssueCredential(issueCredMsg))
		default:
			action.Stop(nil)
		}
	}
}

func readWACIIssuanceData(store storage.Store, id string, newID string) (*waciIssuanceData, error) {
	data, err := store.Get(getWACIIssuanceDataStoreKeyPrefix(id))
	if err != nil {
		return nil, err
	}

	var waciData waciIssuanceData
	err = json.Unmarshal(data, &waciData)
	if err != nil {
		return nil, err
	}

	if newID != "" {
		err = store.Put(getWACIIssuanceDataStoreKeyPrefix(newID), data)
		if err != nil {
			return nil, err
		}
	}

	return &waciData, nil
}

func createOfferCredentialMsg(manifest, responseVP []byte) (*issuecredential.OfferCredentialParams, error) {
	var credentialManifest cm.CredentialManifest

	err := json.Unmarshal(manifest, &credentialManifest)
	if err != nil {
		return nil, err
	}

	vp, err := verifiable.ParsePresentation([]byte(responseVP), verifiable.WithPresJSONLDDocumentLoader(ld.NewDefaultDocumentLoader(nil)))
	if err != nil {
		return nil, err
	}

	attachID1, attachID2 := uuid.New().String(), uuid.New().String()
	format1, format2 := "dif/credential-manifest/manifest@v1.0", "dif/credential-manifest/response@v1.0"

	return &issuecredential.OfferCredentialParams{
		Type:    issuecredential.OfferCredentialMsgTypeV2,
		Comment: "Offer to issue University Degree Credential for Mr.Smith",
		Formats: []issuecredential.Format{{
			AttachID: attachID1,
			Format:   format1,
		}, {
			AttachID: attachID2,
			Format:   format2,
		}},
		Attachments: []decorator.GenericAttachment{
			{
				ID:        attachID1,
				MediaType: "application/json",
				Format:    format1,
				Data: decorator.AttachmentData{
					JSON: struct {
						Manifest cm.CredentialManifest `json:"credential_manifest,omitempty"`
					}{
						Manifest: credentialManifest,
					},
				},
			},
			{
				ID:        attachID2,
				Format:    format2,
				MediaType: "application/json",
				Data: decorator.AttachmentData{
					JSON: vp,
				},
			},
		},
	}, nil
}

func createIssueCredentialMsg(vp []byte, redirect string) (*issuecredential.IssueCredentialParams, error) {
	attachID := uuid.New().String()

	// change credential ID
	vpStr := strings.ReplaceAll(string(vp), "http://example.gov/credentials/3732", "http://example.gov/credentials/"+uuid.NewString())

	presentation, err := verifiable.ParsePresentation([]byte(vpStr), verifiable.WithPresDisabledProofCheck(),
		verifiable.WithPresJSONLDDocumentLoader(ld.NewDefaultDocumentLoader(nil)))
	if err != nil {
		return nil, err
	}

	format := "dif/credential-manifest/response@v1.0"

	return &issuecredential.IssueCredentialParams{
		Type: issuecredential.IssueCredentialMsgTypeV2,
		Formats: []issuecredential.Format{{
			AttachID: attachID,
			Format:   format,
		}},
		Attachments: []decorator.GenericAttachment{{
			ID:        attachID,
			Format:    format,
			MediaType: "application/ld+json",
			Data: decorator.AttachmentData{
				JSON: presentation,
			},
		}},
		WebRedirect: &decorator.WebRedirect{
			Status: "OK",
			URL:    redirect,
		},
	}, nil
}

func getAuthStateKeyPrefix(key string) string {
	return fmt.Sprintf("authstate_%s", key)
}

func getAuthCodeKeyPrefix(key string) string {
	return fmt.Sprintf("authcode_%s", key)
}

func getAccessTokenKeyPrefix(key string) string {
	return fmt.Sprintf("access_token_%s", key)
}

func getCredStoreKeyPrefix(key, credType string) string {
	return fmt.Sprintf("cred_store_%s_%s", key, credType)
}

func getWACIIssuanceDataStoreKeyPrefix(key string) string {
	return fmt.Sprintf("waci_issuance_data_%s", key)
}

func setOIDCResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func sendOIDCErrorResponse(w http.ResponseWriter, msg string, status int) {
	w.WriteHeader(status)
	w.Write([]byte(fmt.Sprintf(`{"error": "%s"}`, msg)))
}

func signCredentialWithED25519(vc *verifiable.Credential) error {
	edPriv := ed25519.PrivateKey(base58.Decode(pkBase58))
	edSigner := &edd25519Signer{edPriv}
	sigSuite := ed25519signature2018.New(suite.WithSigner(edSigner))

	tt := time.Now()

	ldpContext := &verifiable.LinkedDataProofContext{
		SignatureType:           "Ed25519Signature2018",
		SignatureRepresentation: verifiable.SignatureProofValue,
		Suite:                   sigSuite,
		VerificationMethod:      kid,
		Purpose:                 "assertionMethod",
		Created:                 &tt,
	}

	return vc.AddLinkedDataProof(ldpContext, jsonld.WithDocumentLoader(ld.NewDefaultDocumentLoader(nil)))
}

func signJWTCredentialWithED25519(claims *verifiable.JWTCredClaims) (string, error) {
	edPriv := ed25519.PrivateKey(base58.Decode(pkBase58))
	edSigner := &edd25519Signer{edPriv}

	return claims.MarshalJWS(verifiable.EdDSA, edSigner, kid)
}

func signPresentationWithED25519(vc *verifiable.Presentation) error {
	edPriv := ed25519.PrivateKey(base58.Decode(pkBase58))
	edSigner := &edd25519Signer{edPriv}
	sigSuite := ed25519signature2018.New(suite.WithSigner(edSigner))

	tt := time.Now()

	ldpContext := &verifiable.LinkedDataProofContext{
		SignatureType:           "Ed25519Signature2018",
		SignatureRepresentation: verifiable.SignatureProofValue,
		Suite:                   sigSuite,
		VerificationMethod:      kid,
		Purpose:                 "authentication",
		Created:                 &tt,
	}

	return vc.AddLinkedDataProof(ldpContext, jsonld.WithDocumentLoader(ld.NewDefaultDocumentLoader(nil)))
}

func createResponseVP(response []byte, credential []byte, sign bool) (*verifiable.Presentation, error) {
	presentation, err := verifiable.NewPresentation()
	if err != nil {
		return nil, err
	}

	presentation.Context = append(presentation.Context,
		"https://identity.foundation/credential-manifest/response/v1")
	presentation.Type = append(presentation.Type, "CredentialResponse")

	presentation.CustomFields = make(map[string]interface{})

	var responseMap map[string]interface{}
	err = json.Unmarshal(response, &responseMap)
	if err != nil {
		return nil, err
	}

	presentation.CustomFields = responseMap

	cred, err := verifiable.ParseCredential(credential, verifiable.WithJSONLDDocumentLoader(ld.NewDefaultDocumentLoader(nil)),
		verifiable.WithDisabledProofCheck())
	if err != nil {
		return nil, err
	}

	if sign {
		err = signCredentialWithED25519(cred)
		if err != nil {
			return nil, err
		}
	}

	presentation.AddCredentials(cred)

	if sign {
		err = signPresentationWithED25519(presentation)
		if err != nil {
			return nil, err
		}
	}

	return presentation, nil
}

// signer for signing ed25519 for tests.
type edd25519Signer struct {
	privateKey []byte
}

func (s *edd25519Signer) Sign(doc []byte) ([]byte, error) {
	if l := len(s.privateKey); l != ed25519.PrivateKeySize {
		return nil, errors.New("ed25519: bad private key length")
	}

	return ed25519.Sign(s.privateKey, doc), nil
}

func (s *edd25519Signer) Alg() string {
	return ""
}

// generateRandomNumber generates random integer of n digits.
func generateRandomNumber(numberOfDigits int) (int, error) {
	maxLimit := int64(int(math.Pow10(numberOfDigits)) - 1)
	lowLimit := int(math.Pow10(numberOfDigits - 1))

	randomNumber, err := rand.Int(rand.Reader, big.NewInt(maxLimit))
	if err != nil {
		return 0, err
	}
	randomNumberInt := int(randomNumber.Int64())

	// Handling integers between 0, 10^(n-1) .. for n=4, handling cases between (0, 999)
	if randomNumberInt <= lowLimit {
		randomNumberInt += lowLimit
	}

	if randomNumberInt > int(maxLimit) {
		randomNumberInt = int(maxLimit)
	}
	return randomNumberInt, nil
}
