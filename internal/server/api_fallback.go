// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"

	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// notImplemented answers every operation of the contract with 501
// server.not_implemented. It is the bottom layer of api (api.go): an
// operation a domain implements hides the method here.
//
// One method per operation, in the order of httpapi.StrictServerInterface.
// When the contract gains an operation, the assertion below stops compiling
// until the new method is added here (or implemented by a domain).
type notImplemented struct{}

var _ httpapi.StrictServerInterface = notImplemented{}

func (notImplemented) ListAdapters(context.Context, httpapi.ListAdaptersRequestObject) (httpapi.ListAdaptersResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateAdapter(context.Context, httpapi.UpdateAdapterRequestObject) (httpapi.UpdateAdapterResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListAudit(context.Context, httpapi.ListAuditRequestObject) (httpapi.ListAuditResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ExportAudit(context.Context, httpapi.ExportAuditRequestObject) (httpapi.ExportAuditResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetInvite(context.Context, httpapi.GetInviteRequestObject) (httpapi.GetInviteResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) AcceptInvite(context.Context, httpapi.AcceptInviteRequestObject) (httpapi.AcceptInviteResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) Login(context.Context, httpapi.LoginRequestObject) (httpapi.LoginResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) Logout(context.Context, httpapi.LogoutRequestObject) (httpapi.LogoutResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) FinishProviderLogin(context.Context, httpapi.FinishProviderLoginRequestObject) (httpapi.FinishProviderLoginResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) StartProviderLogin(context.Context, httpapi.StartProviderLoginRequestObject) (httpapi.StartProviderLoginResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListCatalog(context.Context, httpapi.ListCatalogRequestObject) (httpapi.ListCatalogResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetCatalogResource(context.Context, httpapi.GetCatalogResourceRequestObject) (httpapi.GetCatalogResourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetCatalogFile(context.Context, httpapi.GetCatalogFileRequestObject) (httpapi.GetCatalogFileResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) SetCatalogPreference(context.Context, httpapi.SetCatalogPreferenceRequestObject) (httpapi.SetCatalogPreferenceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListCheckRules(context.Context, httpapi.ListCheckRulesRequestObject) (httpapi.ListCheckRulesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetCLIPolicy(context.Context, httpapi.GetCLIPolicyRequestObject) (httpapi.GetCLIPolicyResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateCLIPolicy(context.Context, httpapi.UpdateCLIPolicyRequestObject) (httpapi.UpdateCLIPolicyResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListCLIDownloads(context.Context, httpapi.ListCLIDownloadsRequestObject) (httpapi.ListCLIDownloadsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) RequestDeviceCode(context.Context, httpapi.RequestDeviceCodeRequestObject) (httpapi.RequestDeviceCodeResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetDeviceRequest(context.Context, httpapi.GetDeviceRequestRequestObject) (httpapi.GetDeviceRequestResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ApproveDeviceRequest(context.Context, httpapi.ApproveDeviceRequestRequestObject) (httpapi.ApproveDeviceRequestResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) DenyDeviceRequest(context.Context, httpapi.DenyDeviceRequestRequestObject) (httpapi.DenyDeviceRequestResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) PollDeviceToken(context.Context, httpapi.PollDeviceTokenRequestObject) (httpapi.PollDeviceTokenResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListFeedback(context.Context, httpapi.ListFeedbackRequestObject) (httpapi.ListFeedbackResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateFeedback(context.Context, httpapi.CreateFeedbackRequestObject) (httpapi.CreateFeedbackResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetFeedbackSummary(context.Context, httpapi.GetFeedbackSummaryRequestObject) (httpapi.GetFeedbackSummaryResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetFeedback(context.Context, httpapi.GetFeedbackRequestObject) (httpapi.GetFeedbackResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateFeedback(context.Context, httpapi.UpdateFeedbackRequestObject) (httpapi.UpdateFeedbackResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CommentOnFeedback(context.Context, httpapi.CommentOnFeedbackRequestObject) (httpapi.CommentOnFeedbackResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) RateFeedback(context.Context, httpapi.RateFeedbackRequestObject) (httpapi.RateFeedbackResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListIdentityProviders(context.Context, httpapi.ListIdentityProvidersRequestObject) (httpapi.ListIdentityProvidersResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateIdentityProvider(context.Context, httpapi.CreateIdentityProviderRequestObject) (httpapi.CreateIdentityProviderResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListIdentityProviderKinds(context.Context, httpapi.ListIdentityProviderKindsRequestObject) (httpapi.ListIdentityProviderKindsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) DeleteIdentityProvider(context.Context, httpapi.DeleteIdentityProviderRequestObject) (httpapi.DeleteIdentityProviderResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetIdentityProvider(context.Context, httpapi.GetIdentityProviderRequestObject) (httpapi.GetIdentityProviderResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateIdentityProvider(context.Context, httpapi.UpdateIdentityProviderRequestObject) (httpapi.UpdateIdentityProviderResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetInstallsSummary(context.Context, httpapi.GetInstallsSummaryRequestObject) (httpapi.GetInstallsSummaryResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListInvites(context.Context, httpapi.ListInvitesRequestObject) (httpapi.ListInvitesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateInvites(context.Context, httpapi.CreateInvitesRequestObject) (httpapi.CreateInvitesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) RevokeInvite(context.Context, httpapi.RevokeInviteRequestObject) (httpapi.RevokeInviteResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListJoinRequests(context.Context, httpapi.ListJoinRequestsRequestObject) (httpapi.ListJoinRequestsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ApproveJoinRequest(context.Context, httpapi.ApproveJoinRequestRequestObject) (httpapi.ApproveJoinRequestResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) RejectJoinRequest(context.Context, httpapi.RejectJoinRequestRequestObject) (httpapi.RejectJoinRequestResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetMe(context.Context, httpapi.GetMeRequestObject) (httpapi.GetMeResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateMe(context.Context, httpapi.UpdateMeRequestObject) (httpapi.UpdateMeResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListMyDevices(context.Context, httpapi.ListMyDevicesRequestObject) (httpapi.ListMyDevicesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) SignOutDevice(context.Context, httpapi.SignOutDeviceRequestObject) (httpapi.SignOutDeviceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListMyJoinRequests(context.Context, httpapi.ListMyJoinRequestsRequestObject) (httpapi.ListMyJoinRequestsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateJoinRequests(context.Context, httpapi.CreateJoinRequestsRequestObject) (httpapi.CreateJoinRequestsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListMyNotifications(context.Context, httpapi.ListMyNotificationsRequestObject) (httpapi.ListMyNotificationsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) MarkMyNotificationsRead(context.Context, httpapi.MarkMyNotificationsReadRequestObject) (httpapi.MarkMyNotificationsReadResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ChangeMyPassword(context.Context, httpapi.ChangeMyPasswordRequestObject) (httpapi.ChangeMyPasswordResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListMySecrets(context.Context, httpapi.ListMySecretsRequestObject) (httpapi.ListMySecretsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) DeleteMySecret(context.Context, httpapi.DeleteMySecretRequestObject) (httpapi.DeleteMySecretResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) SetMySecret(context.Context, httpapi.SetMySecretRequestObject) (httpapi.SetMySecretResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) SignOutEverywhere(context.Context, httpapi.SignOutEverywhereRequestObject) (httpapi.SignOutEverywhereResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListMyAccessTokens(context.Context, httpapi.ListMyAccessTokensRequestObject) (httpapi.ListMyAccessTokensResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateAccessToken(context.Context, httpapi.CreateAccessTokenRequestObject) (httpapi.CreateAccessTokenResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) RevokeAccessToken(context.Context, httpapi.RevokeAccessTokenRequestObject) (httpapi.RevokeAccessTokenResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetMeta(context.Context, httpapi.GetMetaRequestObject) (httpapi.GetMetaResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListModules(context.Context, httpapi.ListModulesRequestObject) (httpapi.ListModulesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateModule(context.Context, httpapi.UpdateModuleRequestObject) (httpapi.UpdateModuleResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListProducts(context.Context, httpapi.ListProductsRequestObject) (httpapi.ListProductsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateProduct(context.Context, httpapi.CreateProductRequestObject) (httpapi.CreateProductResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetProduct(context.Context, httpapi.GetProductRequestObject) (httpapi.GetProductResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateProduct(context.Context, httpapi.UpdateProductRequestObject) (httpapi.UpdateProductResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListProductMembers(context.Context, httpapi.ListProductMembersRequestObject) (httpapi.ListProductMembersResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) RemoveProductMember(context.Context, httpapi.RemoveProductMemberRequestObject) (httpapi.RemoveProductMemberResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) PutProductMember(context.Context, httpapi.PutProductMemberRequestObject) (httpapi.PutProductMemberResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListResources(context.Context, httpapi.ListResourcesRequestObject) (httpapi.ListResourcesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetResource(context.Context, httpapi.GetResourceRequestObject) (httpapi.GetResourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateResource(context.Context, httpapi.UpdateResourceRequestObject) (httpapi.UpdateResourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) DisableResource(context.Context, httpapi.DisableResourceRequestObject) (httpapi.DisableResourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) DiscardDraft(context.Context, httpapi.DiscardDraftRequestObject) (httpapi.DiscardDraftResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetDraft(context.Context, httpapi.GetDraftRequestObject) (httpapi.GetDraftResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) PutDraft(context.Context, httpapi.PutDraftRequestObject) (httpapi.PutDraftResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UploadDraftBundle(context.Context, httpapi.UploadDraftBundleRequestObject) (httpapi.UploadDraftBundleResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetDraftDiff(context.Context, httpapi.GetDraftDiffRequestObject) (httpapi.GetDraftDiffResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetDraftFile(context.Context, httpapi.GetDraftFileRequestObject) (httpapi.GetDraftFileResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) EnableResource(context.Context, httpapi.EnableResourceRequestObject) (httpapi.EnableResourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetResourceImpact(context.Context, httpapi.GetResourceImpactRequestObject) (httpapi.GetResourceImpactResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) PublishResource(context.Context, httpapi.PublishResourceRequestObject) (httpapi.PublishResourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) RollbackResource(context.Context, httpapi.RollbackResourceRequestObject) (httpapi.RollbackResourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListResourceVersions(context.Context, httpapi.ListResourceVersionsRequestObject) (httpapi.ListResourceVersionsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetResourceVersion(context.Context, httpapi.GetResourceVersionRequestObject) (httpapi.GetResourceVersionResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) WaiveFinding(context.Context, httpapi.WaiveFindingRequestObject) (httpapi.WaiveFindingResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetSettings(context.Context, httpapi.GetSettingsRequestObject) (httpapi.GetSettingsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateSettings(context.Context, httpapi.UpdateSettingsRequestObject) (httpapi.UpdateSettingsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) SendTestMail(context.Context, httpapi.SendTestMailRequestObject) (httpapi.SendTestMailResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetSetup(context.Context, httpapi.GetSetupRequestObject) (httpapi.GetSetupResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateSetupAdmin(context.Context, httpapi.CreateSetupAdminRequestObject) (httpapi.CreateSetupAdminResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CompleteSetup(context.Context, httpapi.CompleteSetupRequestObject) (httpapi.CompleteSetupResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListSources(context.Context, httpapi.ListSourcesRequestObject) (httpapi.ListSourcesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) CreateSource(context.Context, httpapi.CreateSourceRequestObject) (httpapi.CreateSourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) OrderSources(context.Context, httpapi.OrderSourcesRequestObject) (httpapi.OrderSourcesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) DeleteSource(context.Context, httpapi.DeleteSourceRequestObject) (httpapi.DeleteSourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetSource(context.Context, httpapi.GetSourceRequestObject) (httpapi.GetSourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateSource(context.Context, httpapi.UpdateSourceRequestObject) (httpapi.UpdateSourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) PullSource(context.Context, httpapi.PullSourceRequestObject) (httpapi.PullSourceResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListStages(context.Context, httpapi.ListStagesRequestObject) (httpapi.ListStagesResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetBundle(context.Context, httpapi.GetBundleRequestObject) (httpapi.GetBundleResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetDesiredState(context.Context, httpapi.GetDesiredStateRequestObject) (httpapi.GetDesiredStateResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ReportSync(context.Context, httpapi.ReportSyncRequestObject) (httpapi.ReportSyncResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetSealedSecrets(context.Context, httpapi.GetSealedSecretsRequestObject) (httpapi.GetSealedSecretsResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) ListUsers(context.Context, httpapi.ListUsersRequestObject) (httpapi.ListUsersResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetUser(context.Context, httpapi.GetUserRequestObject) (httpapi.GetUserResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) UpdateUser(context.Context, httpapi.UpdateUserRequestObject) (httpapi.UpdateUserResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) DownloadCLI(context.Context, httpapi.DownloadCLIRequestObject) (httpapi.DownloadCLIResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetInstallPowerShell(context.Context, httpapi.GetInstallPowerShellRequestObject) (httpapi.GetInstallPowerShellResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}

func (notImplemented) GetInstallScript(context.Context, httpapi.GetInstallScriptRequestObject) (httpapi.GetInstallScriptResponseObject, error) {
	return nil, httpapi.ErrNotImplemented
}
