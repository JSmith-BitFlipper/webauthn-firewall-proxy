package webauthn_firewall

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"

	log "unknwon.dev/clog/v2"

	"github.com/JSmith-BitFlipper/webauthn-firewall-proxy/db"

	"webauthn/protocol"
)

func logRequest(r *ExtendedRequest) {
	log.Info("%s:\t%s", r.Request.Method, r.Request.URL)
}

func (wfirewall *WebauthnFirewall) prepareJSONResponse(w http.ResponseWriter) {
	// Set the header info
	w.Header().Set("Access-Control-Allow-Origin", wfirewall.FrontendAddress)
	w.Header().Set("Content-Type", "application/json")
}

func (wfirewall *WebauthnFirewall) preamble(w http.ResponseWriter, r *ExtendedRequest) {
	// Print the HTTP request if verbosity is on
	if wfirewall.verbose {
		logRequest(r)
	}

	// Allow transmitting cookies, used by `sessionStore`
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

func (wfirewall *WebauthnFirewall) proxyRequest(w http.ResponseWriter, r *ExtendedRequest) {
	if wfirewall.verbose {
		logRequest(r)
	}

	wfirewall.ServeHTTP(w, r)
}

func (wfirewall *WebauthnFirewall) ProxyRequest(w http.ResponseWriter, r *ExtendedRequest) {
	// If an error has already occured, exit now
	if r.HandleAnyErrors(w) {
		return
	}

	// Refill before proxying onward
	r.Refill()
	wfirewall.proxyRequest(w, r)
}

func (wfirewall *WebauthnFirewall) optionsHandler(allowMethods ...string) HandlerFnType {
	return func(w http.ResponseWriter, r *ExtendedRequest) {
		// Call the firewall preamble
		wfirewall.preamble(w, r)

		// Set the return OPTIONS
		w.Header().Set("Access-Control-Allow-Headers", "Origin,Content-Type,Accept,Authorization")
		w.Header().Set("Access-Control-Allow-Methods", strings.Join(allowMethods, ","))
		w.Header().Set("Access-Control-Allow-Origin", wfirewall.FrontendAddress)

		w.WriteHeader(http.StatusNoContent)
	}
}

func CheckWebauthnAssertion(
	r *ExtendedRequest,
	query db.WebauthnQuery,
	expectedExtensions protocol.AuthenticationExtensions,
	assertion string) error {

	// Get a `webauthnUser` from the input `query`
	wuser, err := db.WebauthnStore.GetWebauthnUser(query)
	if err != nil {
		return err
	}

	// Load the session data
	sessionData, err := sessionStore.GetWebauthnSession("authentication", r.Request)
	if err != nil {
		return err
	}

	// Verify the transaction authentication text
	var verifyTxAuthSimple protocol.ExtensionsVerifier = func(_, clientDataExtensions protocol.AuthenticationExtensions) error {
		if !reflect.DeepEqual(expectedExtensions, clientDataExtensions) {
			return fmt.Errorf("Extensions verification failed: Expected %v, Received %v",
				expectedExtensions,
				clientDataExtensions)
		}

		// Successfully verified the extensions!
		return nil
	}

	// TODO: In an actual implementation, we should perform additional checks on
	// the returned 'credential', i.e. check 'credential.Authenticator.CloneWarning'
	// and then increment the credentials counter
	_, err = webauthnAPI.FinishLogin(wuser, sessionData, verifyTxAuthSimple, assertion)
	if err != nil {
		return err
	}

	// Success!
	return nil
}

func (wfirewall *WebauthnFirewall) webauthnSecure(getAuthnText func(*ExtendedRequest) string) HandlerFnType {
	return func(w http.ResponseWriter, r *ExtendedRequest) {
		// If an error has already occured (usually during some initialization), exit now
		if r.HandleAnyErrors(w) {
			return
		}

		// Call the firewall preamble
		wfirewall.preamble(w, r)

		// Retrieve the `userID` associated with the current request
		userID, err := r.GetUserID()
		if r.HandleError(w, err) {
			return
		}

		// See if the user has webauthn enabled
		isEnabled := db.WebauthnStore.IsUserEnabled(db.QueryByUserID(userID))

		// Perform a webauthn check if webauthn is enabled for this user
		if isEnabled {
			// Parse the form-data to retrieve the `http.Request` information
			assertion, err := r.Get_WithErr("assertion")
			if r.HandleError(w, err) {
				return
			}

			// Get the `authnText` to verify against
			authnText := getAuthnText(r)

			// Check if there were any errors from `getAuthnText`
			if r.HandleAnyErrors(w) {
				return
			}

			// Populate the `extensions` with the `authnText`
			extensions := make(protocol.AuthenticationExtensions)
			extensions["txAuthSimple"] = authnText

			// Check the webauthn assertion for this operation
			err = CheckWebauthnAssertion(r, db.QueryByUserID(userID), extensions, assertion)
			if r.HandleError_WithStatus(w, err, http.StatusBadRequest) {
				return
			}

			// Refill the `request` data before proxying onward
			r.Refill()
		}

		// Once the webauthn check passed, pass the request onward to
		// the server to check the username and password
		wfirewall.ServeHTTP(w, r)
		return
	}
}
