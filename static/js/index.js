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
  var copyButtons = document.querySelectorAll('.js-copy-webhook');
  var hasManual = body.dataset.hasManual === 'true';
  var maxDisplayNameLength = 50;

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

  function setMode(mode) {
    if (mode !== 'renew' || !hasManual) {
      mode = 'onboarding';
    }
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
    } else {
      url.searchParams.delete('mode');
    }
    history.replaceState({}, '', url);
    if (mode === 'renew') {
      refreshManualWizard();
    } else {
      refreshOnboardingWizard();
    }
  }

  function setStepStates(stepElements, progressElements, activeId, mode) {
    var order = ['username', 'authorize', 'webhook'];
    if (stepElements === manualSteps) {
      order = ['select', 'confirm', 'result'];
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
    if (!extra || !extra.result) {
      url.searchParams.delete('result');
      url.searchParams.delete('error');
    }
    history.replaceState({}, '', url);
  }

  modeButtons.forEach(function(btn) {
    btn.addEventListener('click', function() {
      setMode(btn.getAttribute('data-mode-toggle'));
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

  // Initialise mode - prioritize URL/server mode, then results, then localStorage
  var urlParams = new URLSearchParams(window.location.search);
  var urlMode = (urlParams.get('mode') || '').toLowerCase();
  var initialMode = body.dataset.mode || 'onboarding';
  var storedMode = localStorage.getItem('plaxtWizardMode');

  // If URL explicitly has mode=renew and there's a manual result, use renew mode
  if (urlMode === 'renew' && manualResult && hasManual) {
    initialMode = 'renew';
  } else if (urlMode === 'renew' && hasManual) {
    // URL says renew and we have manual users available
    initialMode = 'renew';
  } else if (onboardingResult) {
    // If there's an onboarding result, force onboarding mode
    initialMode = 'onboarding';
  } else if (manualResult && hasManual) {
    // If there's a manual result (without URL mode), force manual renewal mode
    initialMode = 'renew';
  } else if (storedMode && storedMode === 'renew' && hasManual) {
    // Otherwise use stored mode if available
    initialMode = storedMode;
  }
  setMode(initialMode);


  resetOnboardingStateIfComplete();
  resetManualStateIfComplete();

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
})();
