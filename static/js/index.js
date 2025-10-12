(function() {
  var body = document.body;
  var root = body.dataset.root || '';
  var clientId = body.dataset.clientId || '';
  var onboardingResult = (body.dataset.onboardingResult || '').toLowerCase();
  var manualResult = (body.dataset.manualResult || '').toLowerCase();
  var modeButtons = document.querySelectorAll('[data-mode-toggle]');
  var sections = document.querySelectorAll('[data-mode-section]');
  var onboardingForm = document.querySelector('.js-onboarding-form');
  var onboardingInput = document.querySelector('.js-onboarding-username');
  var onboardingError = document.querySelector('.js-onboarding-error');
  var onboardingSummaryName = document.querySelector('.js-onboarding-summary-name');
  var onboardingAuthButton = document.querySelector('.js-onboarding-start');
  var onboardingAuthError = document.querySelector('.js-onboarding-auth-error');
  var onboardingSteps = document.querySelectorAll('[data-mode-section="onboarding"] .wizard-step');
  var onboardingProgress = document.querySelectorAll('[data-mode-section="onboarding"] .wizard-progress-item');
  var manualSelect = document.querySelector('.js-renew-select');
  var manualError = document.querySelector('.js-renew-error');
  var manualContinue = document.querySelector('.js-manual-continue');
  var manualStart = document.querySelector('.js-manual-start');
  var manualSummaryName = document.querySelector('.js-manual-summary-name');
  var manualSummaryDisplay = document.querySelector('.js-manual-summary-display');
  var manualSummaryWebhook = document.querySelector('.js-manual-summary-webhook');
  var manualDisplayForm = document.querySelector('.js-manual-display-form');
  var manualDisplayInput = document.querySelector('.js-manual-display-input');
  var manualDisplayError = document.querySelector('.js-manual-display-error');
  var manualDisplaySuccess = document.querySelector('.js-manual-display-success');
  var manualSteps = document.querySelectorAll('[data-mode-section="renew"] .wizard-step');
  var manualProgress = document.querySelectorAll('[data-mode-section="renew"] .wizard-progress-item');
  var familySteps = document.querySelectorAll('[data-mode-section="family"] .wizard-step');
  var familyProgress = document.querySelectorAll('[data-mode-section="family"] .wizard-progress-item');
  var copyButtons = document.querySelectorAll('.js-copy-webhook');
  var hasManual = body.dataset.hasManual === 'true';
  var maxDisplayNameLength = 50;

  // Telemetry tracking
  function getOnboardingStartTime() {
    var stored = localStorage.getItem('plaxtOnboardingStartTime');
    return stored ? parseInt(stored, 10) : null;
  }

  function setOnboardingStartTime() {
    var now = Date.now();
    localStorage.setItem('plaxtOnboardingStartTime', now.toString());
    return now;
  }

  function calculateDuration() {
    var startTime = getOnboardingStartTime();
    if (!startTime) return 0;
    return Date.now() - startTime;
  }

  function emitTelemetry(event, mode, success, durationMs) {
    // Log to console for debugging
    console.log('[Telemetry]', event, {
      mode: mode,
      success: success,
      duration_ms: durationMs
    });

    // Send to backend
    fetch('/api/telemetry', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        event: event,
        mode: mode,
        success: success,
        duration_ms: durationMs
      })
    }).catch(function(err) {
      // Silently fail - telemetry should not block user experience
      console.error('[Telemetry] Failed to send:', err);
    });
  }

  if (onboardingSummaryName && body.dataset.onboardingUsername) {
    onboardingSummaryName.textContent = body.dataset.onboardingUsername;
  }

  function refreshOnboardingWizard() {
    var result = (body.dataset.onboardingResult || '').toLowerCase();
    var urlParams = new URLSearchParams(window.location.search);
    var stepParam = (urlParams.get('step') || '').toLowerCase();
    var modeParam = (urlParams.get('mode') || '').toLowerCase();
    var target = 'username';

    // Only process onboarding steps if we're in onboarding mode
    if (modeParam !== 'renew') {
      // Check explicit step parameter first (from server redirect)
      if (stepParam === 'webhook') {
        target = 'webhook';
      } else if (stepParam === 'authorize') {
        target = 'authorize';
      } else if (stepParam === 'username') {
        target = 'username';
      } else if (result === 'success') {
        // Fallback: result-based logic
        target = 'webhook';
      } else if (result === 'error' || result === 'cancelled') {
        target = 'authorize';
      } else {
        // Fallback: localStorage-based logic
        var storedStep = localStorage.getItem('plaxtOnboardingStep');
        if (storedStep === 'authorize') {
          target = 'authorize';
        }
      }
    }
    setStepStates(onboardingSteps, onboardingProgress, target, 'onboarding');
  }

  function refreshManualWizard() {
    var result = (body.dataset.manualResult || '').toLowerCase();
    var urlParams = new URLSearchParams(window.location.search);
    var stepParam = (urlParams.get('step') || '').toLowerCase();
    var modeParam = (urlParams.get('mode') || '').toLowerCase();
    var target = 'select';

    // Only process manual renewal steps if we're in renew mode
    if (modeParam === 'renew') {
      // Check explicit step parameter first (from server redirect)
      if (stepParam === 'result') {
        target = 'result';
      } else if (stepParam === 'confirm') {
        target = 'confirm';
      } else if (stepParam === 'select') {
        target = 'select';
      } else if (result === 'success' || result === 'error' || result === 'cancelled') {
        // Fallback: result-based logic
        target = 'result';
      } else {
        // Fallback: localStorage-based logic
        var storedManual = localStorage.getItem('plaxtManualStep');
        if (storedManual === 'confirm' || storedManual === 'result') {
          target = storedManual;
        }
      }
    }
    setStepStates(manualSteps, manualProgress, target, 'renew');
    var resultStep = document.querySelector('[data-mode-section="renew"] .wizard-progress-item[data-step-id="result"]');
    if (resultStep) {
      if (result === 'success') {
        resultStep.setAttribute('data-result', 'success');
      } else if (result === 'error') {
        resultStep.setAttribute('data-result', 'error');
      } else {
        resultStep.removeAttribute('data-result');
      }
    }
  }

  function refreshFamilyWizard() {
    var result = (body.dataset.familyResult || '').toLowerCase();
    var urlParams = new URLSearchParams(window.location.search);
    var stepParam = (urlParams.get('step') || '').toLowerCase();
    var target = 'setup';

    // Check explicit step parameter first (from server redirect)
    if (stepParam === 'webhook') {
      target = 'webhook';
    } else if (stepParam === 'authorize') {
      target = 'authorize';
    } else if (stepParam === 'setup') {
      target = 'setup';
    } else if (result === 'success') {
      // Fallback: result-based logic
      target = 'webhook';
    } else if (result === 'error') {
      target = 'setup';
    } else {
      // Fallback: localStorage-based logic
      var storedFamily = localStorage.getItem('plaxtFamilyStep');
      if (storedFamily === 'authorize' || storedFamily === 'webhook') {
        target = storedFamily;
      }
    }
    setStepStates(familySteps, familyProgress, target, 'family');
  }

  function setMode(mode) {
    if (mode !== 'renew' && mode !== 'family') {
      mode = 'onboarding';
    }
    // Allow renew mode even if no users - show empty state message
    var previousMode = body.dataset.mode;
    body.dataset.mode = mode;
    modeButtons.forEach(function(btn) {
      var target = btn.getAttribute('data-mode-toggle');
      btn.classList.toggle('is-active', target === mode);
    });
    sections.forEach(function(section) {
      section.classList.toggle('mode-hidden', section.getAttribute('data-mode-section') !== mode);
    });
    localStorage.setItem('plaxtWizardMode', mode);
    var url = new URL(window.location.href);
    if (mode === 'renew' && hasManual) {
      url.searchParams.set('mode', 'renew');
    } else if (mode === 'family') {
      url.searchParams.set('mode', 'family');
    } else {
      url.searchParams.delete('mode');
    }
    history.replaceState({}, '', url);
    if (mode === 'renew') {
      refreshManualWizard();
    } else if (mode === 'family') {
      // Only clear family state when switching FROM another mode TO family mode
      // Don't clear if already in family mode (e.g., page reload or navigation within wizard)
      if (previousMode && previousMode !== 'family') {
        localStorage.removeItem('plaxtFamilyStep');
        sessionStorage.removeItem('familyState');
        sessionStorage.removeItem('familyGroupId');
      }
      refreshFamilyWizard();
    } else {
      refreshOnboardingWizard();
    }
  }

  function setStepStates(stepElements, progressElements, activeId, mode) {
    var order = ['username', 'authorize', 'webhook'];
    if (stepElements === manualSteps) {
      order = ['select', 'confirm', 'result'];
    } else if (stepElements === familySteps) {
      order = ['setup', 'authorize', 'webhook'];
    }
    order.forEach(function(stepId, idx) {
      var state = 'future';
      if (stepId === activeId) {
        state = 'active';
      } else {
        var activeIndex = order.indexOf(activeId);
        if (activeIndex !== -1 && idx < activeIndex) {
          state = 'complete';
        }
      }
      stepElements.forEach(function(step) {
        if (step.getAttribute('data-step-id') === stepId) {
          step.setAttribute('data-state', state);
        }
      });
      progressElements.forEach(function(item) {
        if (item.getAttribute('data-step-id') === stepId) {
          item.setAttribute('data-state', state);
        }
      });
    });
    return activeId;
  }

  function resetOnboardingStateIfComplete() {
    var urlParams = new URLSearchParams(window.location.search);
    var urlMode = (urlParams.get('mode') || '').toLowerCase();
    // Only reset state if we're actually in onboarding mode (not manual renewal)
    if (body.dataset.onboardingResult && urlMode !== 'renew') {
      localStorage.removeItem('plaxtOnboardingStep');
      localStorage.removeItem('plaxtOnboardingUsername');
      localStorage.removeItem('plaxtManualStep');
      localStorage.removeItem('plaxtManualSelected');
      localStorage.removeItem('plaxtManualSelectedName');
      localStorage.removeItem('plaxtManualSelectedUsername');
      localStorage.removeItem('plaxtManualSelectedDisplayName');
      localStorage.setItem('plaxtWizardMode', 'onboarding');
    }
  }

  function resetManualStateIfComplete() {
    if (body.dataset.manualResult) {
      localStorage.removeItem('plaxtManualStep');
      localStorage.removeItem('plaxtManualSelected');
      localStorage.removeItem('plaxtManualSelectedName');
      localStorage.removeItem('plaxtManualSelectedUsername');
      localStorage.removeItem('plaxtManualSelectedDisplayName');
    }
  }

  function resetFamilyStateIfComplete() {
    // Only reset state if we're actually in family mode
    if (body.dataset.familyResult) {
      localStorage.removeItem('plaxtFamilyStep');
      sessionStorage.removeItem('familyState');
      sessionStorage.removeItem('familyGroupId');
      localStorage.setItem('plaxtWizardMode', 'family');
    }
  }

  function buildUserLabel(username, displayName) {
    var trimmedUsername = (username || '').trim();
    var trimmedDisplay = (displayName || '').trim();
    if (!trimmedUsername && !trimmedDisplay) {
      return '';
    }
    if (trimmedDisplay) {
      return trimmedUsername + ' (' + trimmedDisplay + ')';
    }
    return trimmedUsername;
  }

  function startAuthorization(username, id, mode) {
    var payload = {
      mode: mode,
      username: (username || '').trim()
    };
    if (mode === 'renew') {
      payload.id = id || '';
    }
    return fetch('/oauth/state', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).then(function(response) {
      return response.json().catch(function() { return {}; }).then(function(data) {
        if (!response.ok || !data.state) {
          var message = (data && data.error) ? data.error : 'Unable to start authorization. Please try again.';
          throw new Error(message);
        }
        var redirectPath = mode === 'renew' ? '/manual/authorize' : '/authorize';
        var redirectUri = root + redirectPath;
        var params = [
          'client_id=' + encodeURIComponent(clientId),
          'redirect_uri=' + encodeURIComponent(redirectUri),
          'response_type=code',
          'state=' + encodeURIComponent(data.state)
        ];
        return 'https://trakt.tv/oauth/authorize?' + params.join('&');
      });
    });
  }

  function updateURLForStep(step, extra) {
    var url = new URL(window.location.href);
    if (step) {
      url.searchParams.set('step', step);
    } else {
      url.searchParams.delete('step');
    }
    if (extra && extra.id) {
      url.searchParams.set('id', extra.id);
    }
    if (extra && extra.username) {
      url.searchParams.set('username', extra.username);
    }
    if (extra && extra.mode) {
      url.searchParams.set('mode', extra.mode);
    }
    if (extra && extra.family_group_id) {
      url.searchParams.set('family_group_id', extra.family_group_id);
    }
    if (!extra || !extra.result) {
      url.searchParams.delete('result');
      url.searchParams.delete('error');
    }
    history.replaceState({}, '', url);
  }

  modeButtons.forEach(function(btn) {
    btn.addEventListener('click', function() {
      var mode = btn.getAttribute('data-mode-toggle');
      // Set the mode in URL parameters to ensure backend knows the selected mode
      var url = new URL(window.location.href);
      if (mode === 'renew') {
        url.searchParams.set('mode', 'renew');
      } else {
        url.searchParams.delete('mode');
      }
      history.replaceState({}, '', url);
      setMode(mode);
    });
  });

  if (onboardingForm) {
    onboardingForm.addEventListener('submit', function(event) {
      event.preventDefault();
      var rawUsername = '';
      if (onboardingInput) {
        rawUsername = onboardingInput.value.trim();
      }
      var normalizedUsername = rawUsername.toLowerCase();
      if (!normalizedUsername) {
        onboardingError.textContent = 'Please enter your Plex username.';
        onboardingInput && onboardingInput.focus();
        return;
      }
      onboardingError.textContent = '';
      if (onboardingAuthError) {
        onboardingAuthError.textContent = '';
      }
      body.dataset.onboardingUsername = rawUsername;
      if (onboardingSummaryName) {
        onboardingSummaryName.textContent = rawUsername;
      }

      // Emit telemetry: onboarding_start for individual account
      if (!getOnboardingStartTime()) {
        setOnboardingStartTime();
        emitTelemetry('onboarding_start', 'individual', null, 0);
      }

      localStorage.setItem('plaxtOnboardingStep', 'authorize');
      localStorage.setItem('plaxtOnboardingUsername', normalizedUsername);
      updateURLForStep('authorize', { username: normalizedUsername, mode: 'onboarding' });
      setStepStates(onboardingSteps, onboardingProgress, 'authorize', 'onboarding');
      setMode('onboarding');
      if (onboardingAuthButton) {
        onboardingAuthButton.focus();
      }
    });
  }

  if (onboardingAuthButton) {
    onboardingAuthButton.addEventListener('click', function() {
      var storedUsername = (body.dataset.onboardingUsername || '').trim();
      if (!storedUsername && onboardingInput) {
        storedUsername = onboardingInput.value.trim();
      }
      if (!storedUsername) {
        if (onboardingAuthError) {
          onboardingAuthError.textContent = 'Please enter your Plex username first.';
        }
        setStepStates(onboardingSteps, onboardingProgress, 'username', 'onboarding');
        if (onboardingInput) {
          onboardingInput.focus();
        }
        return;
      }
      var normalizedUsername = storedUsername.toLowerCase();
      if (onboardingAuthError) {
        onboardingAuthError.textContent = '';
      }
      localStorage.setItem('plaxtOnboardingStep', 'authorize');
      localStorage.setItem('plaxtOnboardingUsername', normalizedUsername);
      updateURLForStep('authorize', { username: normalizedUsername, mode: 'onboarding' });
      setStepStates(onboardingSteps, onboardingProgress, 'authorize', 'onboarding');
      startAuthorization(normalizedUsername, '', 'onboarding')
        .then(function(authUrl) {
          window.location = authUrl;
        })
        .catch(function(error) {
          if (onboardingAuthError) {
            onboardingAuthError.textContent = error.message || 'Unable to contact Trakt. Please try again.';
          }
          setStepStates(onboardingSteps, onboardingProgress, 'authorize', 'onboarding');
        });
    });
  }

  if (manualContinue && manualSelect) {
    manualContinue.addEventListener('click', function(event) {
      event.preventDefault();
      var option = manualSelect.options[manualSelect.selectedIndex];
      if (!option || !option.value) {
        manualError.textContent = 'Choose a user before continuing.';
        manualSelect.focus();
        return;
      }
      manualError.textContent = '';
      var username = (option.getAttribute('data-username') || '').trim();
      var traktName = option.getAttribute('data-display-name') || '';
      var label = buildUserLabel(username, traktName);
      var usernameKey = username.toLowerCase();
      var webhook = option.getAttribute('data-webhook') || '';
      if (manualSummaryName) {
        manualSummaryName.textContent = label || '—';
      }
      if (manualSummaryDisplay) {
        manualSummaryDisplay.textContent = traktName.trim() || '—';
      }
      if (manualSummaryWebhook) {
        manualSummaryWebhook.textContent = webhook || '—';
      }
      localStorage.setItem('plaxtManualStep', 'confirm');
      localStorage.setItem('plaxtManualSelected', option.value);
      localStorage.setItem('plaxtManualSelectedUsername', username);
      localStorage.setItem('plaxtManualSelectedDisplayName', traktName);
      localStorage.removeItem('plaxtManualSelectedName');
      setStepStates(manualSteps, manualProgress, 'confirm', 'renew');
      setMode('renew');
      updateURLForStep('', { mode: 'renew', id: option.value, username: usernameKey });
    });
  }

  if (manualStart && manualSelect) {
    manualStart.addEventListener('click', function() {
      var option = manualSelect.options[manualSelect.selectedIndex];
      if (!option || !option.value) {
        manualError.textContent = 'Choose a user before continuing.';
        manualSelect.focus();
        return;
      }
      manualError.textContent = '';
      var username = localStorage.getItem('plaxtManualSelectedUsername') || option.getAttribute('data-username') || '';
      var traktName = localStorage.getItem('plaxtManualSelectedDisplayName') || option.getAttribute('data-display-name') || '';
      var label = buildUserLabel(username, traktName);
      var usernameKey = (username || '').toLowerCase();
      var selectedId = option.value;
      if (manualSummaryName) {
        manualSummaryName.textContent = label || '—';
      }
      if (manualSummaryDisplay) {
        manualSummaryDisplay.textContent = (traktName || '').trim() || '—';
      }
      setStepStates(manualSteps, manualProgress, 'result', 'renew');
      startAuthorization(usernameKey, selectedId, 'renew')
        .then(function(authUrl) {
          localStorage.setItem('plaxtManualStep', 'result');
          localStorage.setItem('plaxtManualSelectedUsername', username);
          localStorage.setItem('plaxtManualSelectedDisplayName', traktName);
          updateURLForStep('', { mode: 'renew', id: selectedId, username: usernameKey });
          window.location = authUrl;
        })
        .catch(function(error) {
          manualError.textContent = error.message || 'Unable to contact Trakt. Please try again.';
          setStepStates(manualSteps, manualProgress, 'confirm', 'renew');
        });
    });
  }

  if (manualDisplayForm && manualDisplayInput) {
    manualDisplayForm.addEventListener('submit', function(event) {
      event.preventDefault();
      if (manualDisplayError) {
        manualDisplayError.textContent = '';
      }
      if (manualDisplaySuccess) {
        manualDisplaySuccess.textContent = '';
      }
      var value = manualDisplayInput.value.trim();
      if (value.length > maxDisplayNameLength) {
        manualDisplayError && (manualDisplayError.textContent = 'Display name must be 50 characters or fewer.');
        manualDisplayInput.focus();
        return;
      }
      var userId = manualDisplayForm.getAttribute('data-user-id') || '';
      if (!userId) {
        manualDisplayError && (manualDisplayError.textContent = 'Missing user identifier. Refresh and try again.');
        return;
      }
      var payload = { display_name: value };
      fetch('/users/' + encodeURIComponent(userId) + '/trakt-display-name', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      }).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) {
            throw new Error(text || 'Unable to save display name.');
          });
        }
        return response.json();
      }).then(function(data) {
        var savedName = (data && data.display_name) ? data.display_name.trim() : '';
        var storedUsername = localStorage.getItem('plaxtManualSelectedUsername') || '';
        if (!storedUsername && manualSelect) {
          var selectedOption = manualSelect.options[manualSelect.selectedIndex];
          if (selectedOption) {
            storedUsername = selectedOption.getAttribute('data-username') || '';
          }
        }
        var label = buildUserLabel(storedUsername, savedName);
        if (manualSummaryDisplay) {
          manualSummaryDisplay.textContent = savedName || '—';
        }
        if (manualSummaryName && label) {
          manualSummaryName.textContent = label;
        }
        localStorage.setItem('plaxtManualSelectedDisplayName', savedName);
        if (storedUsername) {
          localStorage.setItem('plaxtManualSelectedUsername', storedUsername);
        }
        manualDisplayInput.value = savedName;
        body.dataset.manualDisplayName = savedName;
        body.dataset.manualDisplayMissing = savedName ? 'false' : 'true';
        var successMessage = savedName ? 'Display name saved.' : 'Display name cleared.';
        if (data && data.truncated) {
          successMessage = 'Display name saved (truncated to 50 characters).';
          body.dataset.manualDisplayWarning = 'truncated';
        } else {
          body.dataset.manualDisplayWarning = '';
        }
        manualDisplaySuccess && (manualDisplaySuccess.textContent = successMessage);
        if (savedName) {
          var readonly = manualDisplayForm.parentNode.querySelector('.js-manual-display-readonly');
          if (!readonly) {
            readonly = document.createElement('p');
            readonly.className = 'js-manual-display-readonly';
            manualDisplayForm.parentNode.appendChild(readonly);
          }
          readonly.innerHTML = '<strong>Trakt account:</strong> ' + savedName;
          manualDisplayForm.style.display = 'none';
        } else {
          var existing = manualDisplayForm.parentNode.querySelector('.js-manual-display-readonly');
          if (existing && existing.parentNode) {
            existing.parentNode.removeChild(existing);
          }
          manualDisplayForm.style.display = '';
        }
      }).catch(function(error) {
        manualDisplayError && (manualDisplayError.textContent = error.message || 'Unable to save display name.');
      });
    });
  }

  copyButtons.forEach(function(btn) {
    btn.addEventListener('click', function() {
      var targetSelector = btn.getAttribute('data-copy-target');
      if (!targetSelector) {
        return;
      }
      var target = document.querySelector(targetSelector);
      if (!target) {
        return;
      }
      var text = target.textContent.trim();
      if (!navigator.clipboard) {
        try {
          var textarea = document.createElement('textarea');
          textarea.value = text;
          document.body.appendChild(textarea);
          textarea.select();
          document.execCommand('copy');
          document.body.removeChild(textarea);
        } catch (err) {
          return;
        }
      } else {
        navigator.clipboard.writeText(text).catch(function() {});
      }
      btn.textContent = 'Copied!';
      setTimeout(function() { btn.textContent = 'Copy webhook URL'; }, 2000);
    });
  });

  // Restore persisted state when returning without result
  if (!body.dataset.manualResult) {
    var storedManual = localStorage.getItem('plaxtManualStep');
    var storedSelected = localStorage.getItem('plaxtManualSelected');
    var storedUsername = localStorage.getItem('plaxtManualSelectedUsername') || localStorage.getItem('plaxtManualSelectedName');
    var storedDisplay = localStorage.getItem('plaxtManualSelectedDisplayName') || '';
    if (storedSelected && manualSelect) {
      Array.prototype.forEach.call(manualSelect.options, function(option) {
        if (option.value === storedSelected) {
          option.selected = true;
        }
      });
    }
    if (storedManual === 'confirm') {
      var label = buildUserLabel(storedUsername || '', storedDisplay || '');
      if (manualSummaryName && label) {
        manualSummaryName.textContent = label;
      }
      if (manualSummaryDisplay && storedDisplay) {
        manualSummaryDisplay.textContent = storedDisplay;
      }
    }
  }

  refreshOnboardingWizard();
  refreshManualWizard();
  refreshFamilyWizard();

  // Emit telemetry for completed onboarding
  if (onboardingResult === 'success') {
    var urlParams = new URLSearchParams(window.location.search);
    var urlMode = (urlParams.get('mode') || '').toLowerCase();
    var mode = (urlMode === 'family') ? 'family' : 'individual';
    var duration = calculateDuration();
    if (duration > 0) {
      emitTelemetry('onboarding_complete', mode, true, duration);
      localStorage.removeItem('plaxtOnboardingStartTime');
    }
  }

  // Initialise mode - prioritize URL/server mode, then results, then localStorage
  urlParams = new URLSearchParams(window.location.search);
  urlMode = (urlParams.get('mode') || '').toLowerCase();
  var initialMode = body.dataset.mode || 'onboarding';
  var storedMode = localStorage.getItem('plaxtWizardMode');

  // If URL explicitly has mode=renew and there's a manual result, use renew mode
  if (urlMode === 'renew' && manualResult && hasManual) {
    initialMode = 'renew';
  } else if (urlMode === 'renew' && hasManual) {
    // URL says renew and we have manual users available
    initialMode = 'renew';
  } else if (urlMode === 'family') {
    // URL explicitly says family mode
    initialMode = 'family';
  } else if (onboardingResult) {
    // If there's an onboarding result, force onboarding mode
    initialMode = 'onboarding';
  } else if (manualResult && hasManual) {
    // If there's a manual result (without URL mode), force manual renewal mode
    initialMode = 'renew';
  } else if (storedMode && storedMode === 'renew' && hasManual) {
    // Otherwise use stored mode if available
    initialMode = storedMode;
  } else if (storedMode && storedMode === 'family') {
    // Use stored family mode if available
    initialMode = storedMode;
  }
  setMode(initialMode);

  // Load family members if we're on the authorize step
  var urlParams = new URLSearchParams(window.location.search);
  var familyGroupId = urlParams.get('family_group_id');
  if (familyGroupId && body.dataset.mode === 'family') {
    loadFamilyMembers(familyGroupId);
  }

  resetOnboardingStateIfComplete();
  resetManualStateIfComplete();
  resetFamilyStateIfComplete();

  // "Start Over" button for manual renewal reset
  var resetButton = document.querySelector('.js-reset-manual');
  if (resetButton) {
    resetButton.addEventListener('click', function() {
      // Clear local storage
      localStorage.removeItem('plaxtManualStep');
      localStorage.removeItem('plaxtManualSelected');
      localStorage.removeItem('plaxtManualSelectedName');
      localStorage.removeItem('plaxtManualSelectedUsername');
      localStorage.removeItem('plaxtManualSelectedDisplayName');

      // Reset URL to manual renewal mode without any result params
      var url = new URL(window.location.href);
      url.searchParams.set('mode', 'renew');
      url.searchParams.delete('result');
      url.searchParams.delete('error');
      url.searchParams.delete('step');
      url.searchParams.delete('correlation_id');
      url.searchParams.delete('id');
      url.searchParams.delete('username');
      url.searchParams.delete('display_name');
      url.searchParams.delete('display_name_missing');
      url.searchParams.delete('display_name_warning');

      // Navigate to clean state
      window.location.href = url.toString();
    });
  }

  var resetSuccessButtons = document.querySelectorAll('.js-reset-manual-success');
  resetSuccessButtons.forEach(function(btn) {
    btn.addEventListener('click', function() {
      localStorage.removeItem('plaxtManualStep');
      localStorage.removeItem('plaxtManualSelected');
      localStorage.removeItem('plaxtManualSelectedName');
      localStorage.removeItem('plaxtManualSelectedUsername');
      localStorage.removeItem('plaxtManualSelectedDisplayName');

      var url = new URL(window.location.href);
      url.searchParams.set('mode', 'renew');
      url.searchParams.delete('result');
      url.searchParams.delete('error');
      url.searchParams.delete('step');
      url.searchParams.delete('correlation_id');
      url.searchParams.delete('id');
      url.searchParams.delete('username');
      url.searchParams.delete('display_name');
      url.searchParams.delete('display_name_missing');
      url.searchParams.delete('display_name_warning');

      window.location.href = url.toString();
    });
  });

  // Onboarding reset control
  var onboardingResetButton = document.querySelector('.js-reset-onboarding');
  if (onboardingResetButton) {
    onboardingResetButton.addEventListener('click', function() {
      localStorage.removeItem('plaxtOnboardingStep');
      localStorage.removeItem('plaxtOnboardingUsername');
      localStorage.setItem('plaxtWizardMode', 'onboarding');

      var url = new URL(window.location.href);
      url.searchParams.delete('result');
      url.searchParams.delete('error');
      url.searchParams.delete('step');
      url.searchParams.delete('username');
      url.searchParams.delete('id');
      url.searchParams.delete('mode');
      window.location.href = url.toString();
    });
  }

  // Family Account Wizard Logic
  var familyForm = document.querySelector('.js-family-form');
  var familyPlexInput = document.querySelector('.js-family-plex-username');
  var familyError = document.querySelector('.js-family-error');
  var familyAuthError = document.querySelector('.js-family-auth-error');
  var memberList = document.querySelector('.js-member-list');
  var addMemberButton = document.querySelector('.js-add-member');
  var familyCompleteButton = document.querySelector('.js-family-complete');
  var authProgress = document.querySelector('.js-auth-progress');

  function showFamilyError(message) {
    if (familyError) {
      familyError.textContent = message;
    }
  }

  function showFamilyAuthError(message) {
    if (familyAuthError) {
      familyAuthError.textContent = message;
    }
  }

  function escapeHtml(text) {
    var div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  function loadFamilyMembers(groupId) {
    if (!groupId) return;
    
    fetch('/admin/api/family-groups/' + encodeURIComponent(groupId))
      .then(function(response) {
        if (!response.ok) {
          throw new Error('Failed to load family group');
        }
        return response.json();
      })
      .then(function(data) {
        if (data.members && data.members.length > 0) {
          renderFamilyMembers(data.members);
        }
      })
      .catch(function(err) {
        console.error('Failed to load family members:', err);
        showFamilyAuthError('Failed to load family members');
      });
  }

  function renderFamilyMembers(members) {
    var memberAuthList = document.querySelector('.js-member-auth-list');
    if (!memberAuthList) return;

    memberAuthList.innerHTML = members.map(function(member) {
      var statusText = 'Pending';
      var statusClass = 'status-pending';
      var actionHtml = '';

      if (member.authorization_status === 'authorized') {
        statusText = '✓ Authorized';
        statusClass = 'status-authorized';
        actionHtml = '<span class="text-success">' + (member.trakt_username || '') + '</span>';
      } else if (member.authorization_status === 'failed') {
        statusText = '✗ Failed';
        statusClass = 'status-failed';
        actionHtml = '<button type="button" class="button-secondary js-authorize-member" data-member-id="' + member.id + '">Retry</button>';
      } else {
        actionHtml = '<button type="button" class="button-secondary js-authorize-member" data-member-id="' + member.id + '">Authorize</button>';
      }

      return '<tr data-member-id="' + member.id + '">' +
        '<td><strong>' + escapeHtml(member.temp_label) + '</strong></td>' +
        '<td><span class="status-badge ' + statusClass + ' js-member-status">' + statusText + '</span></td>' +
        '<td>' + actionHtml + '</td>' +
        '</tr>';
    }).join('');

    // Update counter after rendering members
    checkAllAuthorized();
  }

  function updateMemberButtons() {
    if (!memberList) return;
    var items = memberList.querySelectorAll('.member-item');
    var count = items.length;

    items.forEach(function(item) {
      var removeBtn = item.querySelector('.js-remove-member');
      if (removeBtn) {
        removeBtn.disabled = count <= 2;
      }
    });

    if (addMemberButton) {
      addMemberButton.disabled = count >= 10;
    }
  }

  if (addMemberButton) {
    addMemberButton.addEventListener('click', function() {
      if (!memberList) return;
      var count = memberList.querySelectorAll('.member-item').length;

      if (count >= 10) {
        showFamilyError('Maximum 10 members allowed');
        return;
      }

      var item = document.createElement('div');
      item.className = 'member-item';
      item.dataset.index = count;
      item.innerHTML = '<input class="input-field js-member-label" name="member_label" placeholder="e.g., Member ' + (count + 1) + '" maxlength="50" required>' +
        '<button type="button" class="button-icon js-remove-member" title="Remove member">' +
        '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">' +
        '<line x1="18" y1="6" x2="6" y2="18"></line>' +
        '<line x1="6" y1="6" x2="18" y2="18"></line>' +
        '</svg>' +
        '</button>';

      memberList.appendChild(item);
      updateMemberButtons();
      if (familyError) familyError.textContent = '';
    });
  }

  if (memberList) {
    memberList.addEventListener('click', function(e) {
      var removeBtn = e.target.closest('.js-remove-member');
      if (!removeBtn) return;

      var item = removeBtn.closest('.member-item');
      var count = memberList.querySelectorAll('.member-item').length;

      if (count <= 2) {
        showFamilyError('Minimum 2 members required');
        return;
      }

      item.remove();
      updateMemberButtons();
      if (familyError) familyError.textContent = '';
    });
  }

  if (familyForm) {
    familyForm.addEventListener('submit', function(e) {
      e.preventDefault();

      var plexUsername = familyPlexInput ? familyPlexInput.value.trim() : '';
      var labelInputs = document.querySelectorAll('.js-member-label');
      var labels = [];

      labelInputs.forEach(function(input) {
        var label = input.value.trim();
        if (label.length > 0) {
          labels.push(label);
        }
      });

      if (!plexUsername) {
        showFamilyError('Please enter a Plex username');
        if (familyPlexInput) familyPlexInput.focus();
        return;
      }

      if (labels.length < 2) {
        showFamilyError('Minimum 2 family members required');
        return;
      }

      if (labels.length > 10) {
        showFamilyError('Maximum 10 family members allowed');
        return;
      }

      var uniqueLabels = {};
      var hasDuplicates = false;
      labels.forEach(function(label) {
        var lower = label.toLowerCase();
        if (uniqueLabels[lower]) {
          hasDuplicates = true;
        }
        uniqueLabels[lower] = true;
      });

      if (hasDuplicates) {
        showFamilyError('Member labels must be unique');
        return;
      }

      if (familyError) familyError.textContent = '';

      // Emit telemetry: onboarding_start for family account
      if (!getOnboardingStartTime()) {
        setOnboardingStartTime();
        emitTelemetry('onboarding_start', 'family', null, 0);
      }

      fetch('/oauth/family/state', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          mode: 'family',
          plex_username: plexUsername,
          members: labels.map(function(label) {
            return { temp_label: label };
          })
        })
      }).then(function(response) {
        return response.json().then(function(data) {
          if (!response.ok) {
            throw new Error(data.error || 'Failed to create family group');
          }
          return data;
        });
      }).then(function(data) {
        sessionStorage.setItem('familyState', data.state);
        sessionStorage.setItem('familyGroupId', data.family_group_id);
        localStorage.setItem('plaxtFamilyStep', 'authorize');
        updateURLForStep('authorize', { family_group_id: data.family_group_id });
        setStepStates(familySteps, familyProgress, 'authorize', 'family');
        setMode('family');
        // Load family members dynamically
        loadFamilyMembers(data.family_group_id);
      }).catch(function(err) {
        showFamilyError(err.message);
      });
    });
  }

  var memberAuthList = document.querySelector('.js-member-auth-list');
  if (memberAuthList) {
    memberAuthList.addEventListener('click', function(e) {
      var authorizeBtn = e.target.closest('.js-authorize-member');
      if (!authorizeBtn) return;

      var memberId = authorizeBtn.dataset.memberId;
      var state = sessionStorage.getItem('familyState');

      if (!state) {
        showFamilyAuthError('Session expired. Please start over.');
        return;
      }

      var authUrl = root + '/authorize/family/member?state=' + encodeURIComponent(state) + '&member_id=' + encodeURIComponent(memberId) + '&prompt=login';
      window.open(authUrl, '_blank', 'width=600,height=700');

      pollMemberAuthorization(memberId);
    });
  }

  // Listen for postMessage from authorization popup
  window.addEventListener('message', function(event) {
    // Verify origin for security
    if (event.origin !== window.location.origin) return;

    if (event.data && event.data.type === 'family_member_authorized') {
      // Update sessionStorage with new state token
      if (event.data.state) {
        sessionStorage.setItem('familyState', event.data.state);
      }

      // Update member status in UI immediately
      if (event.data.member_id) {
        updateMemberStatus(event.data.member_id, 'authorized', event.data.trakt_username);
      }

      // Check and update counter
      checkAllAuthorized();
    }
  });

  function pollMemberAuthorization(memberId) {
    var groupId = sessionStorage.getItem('familyGroupId');
    if (!groupId) return;

    var maxAttempts = 60;
    var attempts = 0;

    var interval = setInterval(function() {
      attempts++;

      fetch('/api/family-groups/' + encodeURIComponent(groupId) + '/members/' + encodeURIComponent(memberId))
        .then(function(response) {
          if (!response.ok) throw new Error('Failed to check status');
          return response.json();
        })
        .then(function(member) {
          updateMemberStatus(memberId, member.authorization_status, member.trakt_username);

          if (member.authorization_status === 'authorized') {
            clearInterval(interval);
            checkAllAuthorized();
          } else if (member.authorization_status === 'failed') {
            clearInterval(interval);
            showFamilyAuthError('Authorization failed for ' + member.temp_label);
          }

          if (attempts >= maxAttempts) {
            clearInterval(interval);
            showFamilyAuthError('Authorization timeout - please try again');
          }
        })
        .catch(function() {
          clearInterval(interval);
          showFamilyAuthError('Failed to check authorization status');
        });
    }, 2000);
  }

  function updateMemberStatus(memberId, status, traktUsername) {
    var row = document.querySelector('[data-member-id="' + memberId + '"]');
    if (!row) return;

    var statusBadge = row.querySelector('.js-member-status');
    var actionCell = row.querySelector('td:last-child');

    if (statusBadge) {
      statusBadge.className = 'status-badge status-' + status + ' js-member-status';

      if (status === 'authorized') {
        statusBadge.textContent = '✓ Authorized';
      } else if (status === 'failed') {
        statusBadge.textContent = '✗ Failed';
      } else {
        statusBadge.textContent = 'Pending';
      }
    }

    if (actionCell) {
      if (status === 'authorized') {
        actionCell.innerHTML = '<span class="text-success">' + (traktUsername || '') + '</span>';
      } else if (status === 'failed') {
        actionCell.innerHTML = '<button type="button" class="button-secondary js-authorize-member" data-member-id="' + memberId + '">Retry</button>';
      }
    }
  }

  function checkAllAuthorized() {
    var statusBadges = document.querySelectorAll('.js-member-status');
    var allAuthorized = true;
    var authorizedCount = 0;

    statusBadges.forEach(function(badge) {
      if (badge.classList.contains('status-authorized')) {
        authorizedCount++;
      } else {
        allAuthorized = false;
      }
    });

    if (authProgress) {
      authProgress.textContent = authorizedCount + ' of ' + statusBadges.length + ' members authorized';
    }

    if (familyCompleteButton) {
      familyCompleteButton.disabled = !allAuthorized;
    }

    if (allAuthorized) {
      var groupId = sessionStorage.getItem('familyGroupId');
      localStorage.setItem('plaxtFamilyStep', 'webhook');
      // Reload the page to get fresh data from server with webhook URL and member list
      window.location.href = '/?step=webhook&family_group_id=' + encodeURIComponent(groupId) + '&mode=family';
    }
  }

  var resetFamilyButton = document.querySelector('.js-reset-family');
  if (resetFamilyButton) {
    resetFamilyButton.addEventListener('click', function() {
      sessionStorage.removeItem('familyState');
      sessionStorage.removeItem('familyGroupId');
      localStorage.removeItem('plaxtFamilyStep');
      localStorage.setItem('plaxtWizardMode', 'family');

      var url = new URL(window.location.href);
      url.searchParams.delete('result');
      url.searchParams.delete('error');
      url.searchParams.delete('step');
      window.location.href = url.toString();
    });
  }

  updateMemberButtons();

  // Tooltip functionality
  var tooltipTriggers = document.querySelectorAll('.tooltip-trigger');
  tooltipTriggers.forEach(function(trigger) {
    var tooltipId = trigger.getAttribute('data-tooltip-for');
    var tooltip = document.querySelector('[data-tooltip-id="' + tooltipId + '"]');

    if (!tooltip) return;

    // Show tooltip on click
    trigger.addEventListener('click', function(e) {
      e.stopPropagation();

      // Hide all other tooltips
      document.querySelectorAll('.tooltip.is-visible').forEach(function(t) {
        if (t !== tooltip) {
          t.classList.remove('is-visible');
        }
      });

      // Toggle this tooltip
      tooltip.classList.toggle('is-visible');
    });

    // Show tooltip on hover
    trigger.addEventListener('mouseenter', function() {
      tooltip.classList.add('is-visible');
    });

    trigger.addEventListener('mouseleave', function() {
      tooltip.classList.remove('is-visible');
    });

    // Keyboard accessibility
    trigger.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        tooltip.classList.toggle('is-visible');
      }
      if (e.key === 'Escape') {
        tooltip.classList.remove('is-visible');
      }
    });
  });

  // Close tooltips when clicking outside
  document.addEventListener('click', function() {
    document.querySelectorAll('.tooltip.is-visible').forEach(function(tooltip) {
      tooltip.classList.remove('is-visible');
    });
  });

  // Prevent tooltip clicks from bubbling
  document.querySelectorAll('.tooltip').forEach(function(tooltip) {
    tooltip.addEventListener('click', function(e) {
      e.stopPropagation();
    });
  });
})();
