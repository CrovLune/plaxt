const refreshInterval = 5000;
let lastUpdate = Date.now();

async function fetchQueueStatus() {
  try {
    const response = await fetch('/admin/api/queue/status');
    if (!response.ok) throw new Error('Failed to fetch');
    const data = await response.json();
    updateUI(data);
    lastUpdate = Date.now();
    updateLastUpdatedTime();
  } catch (error) {
    console.error('Error fetching queue status:', error);
    document.getElementById('last-updated').textContent = 'Error loading data';
  }
}

async function fetchQueueEvents() {
  try {
    const response = await fetch('/admin/api/queue/events');
    if (!response.ok) throw new Error('Failed to fetch');
    const data = await response.json();
    updateEventLog(data.events);
  } catch (error) {
    console.error('Error fetching queue events:', error);
  }
}

function updateUI(data) {
  document.getElementById('total-users').textContent = data.system.total_users;
  document.getElementById('active-queues').textContent = data.system.users_with_queues;
  document.getElementById('total-events').textContent = data.system.total_events;
  document.getElementById('system-mode').textContent = `${data.system.mode.toUpperCase()} 🔴`;

  const tbody = document.getElementById('queue-table-body');

  if (data.users.length === 0) {
    tbody.innerHTML = `
      <tr>
        <td colspan="5" class="empty-state">
          <div class="empty-state-icon">✅</div>
          <div>No users found</div>
        </td>
      </tr>
    `;
    return;
  }

  tbody.innerHTML = data.users.map(user => {
    const processed = user.events_processed || 0;
    const failed = user.events_failed || 0;
    const progressInfo = user.drain_active ? `${processed} / ${failed}` : '-';

    return `
      <tr>
        <td>
          <strong>${escapeHtml(user.username)}</strong>
          ${user.trakt_display_name ? `<br><small style="color: #666;">${escapeHtml(user.trakt_display_name)}</small>` : ''}
        </td>
        <td>${user.queue_size}</td>
        <td>${formatAge(user.oldest_event_age_seconds)}</td>
        <td>${renderStatus(user.status)}</td>
        <td>${progressInfo}</td>
      </tr>
    `;
  }).join('');
}

function updateEventLog(events) {
  const logContainer = document.getElementById('event-log-body');

  if (!logContainer) {
    return;
  }

  if (!events || events.length === 0) {
    logContainer.innerHTML = `
      <tr>
        <td colspan="5" class="empty-state">
          <div class="empty-state-icon">📋</div>
          <div>No recent events</div>
        </td>
      </tr>
    `;
    return;
  }

  logContainer.innerHTML = events.map(event => `
    <tr>
      <td class="event-time">${formatTime(event.timestamp)}</td>
      <td>
        ${escapeHtml(event.username || event.user_id.substring(0, 8))}
      </td>
      <td class="event-operation">${formatOperation(event.operation)}</td>
      <td>
        ${escapeHtml(event.event_id ? event.event_id.substring(0, 8) : '-')}
      </td>
      <td class="event-details">${escapeHtml(event.error || event.details || '-')}</td>
    </tr>
  `).join('');
}

function formatOperation(op) {
  return op.replace('queue_', '').replace(/_/g, ' ');
}

function renderStatus(status) {
  const statusClasses = {
    'healthy': 'status-healthy',
    'queued': 'status-queued',
    'draining': 'status-draining pulse',
    'stalled': 'status-stalled',
    'errors': 'status-errors'
  };

  return `
    <span class="status-indicator ${statusClasses[status] || ''}">
      <span class="status-dot"></span>
      ${status}
    </span>
  `;
}

function updateLastUpdatedTime() {
  const seconds = Math.floor((Date.now() - lastUpdate) / 1000);
  document.getElementById('last-updated').textContent =
    seconds === 0 ? 'Just updated' : `Updated ${seconds}s ago`;
}

fetchQueueStatus();
fetchQueueEvents();

setInterval(fetchQueueStatus, refreshInterval);
setInterval(fetchQueueEvents, refreshInterval);
setInterval(updateLastUpdatedTime, 1000);
