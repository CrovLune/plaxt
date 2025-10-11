let users = [];

// Load users on page load
document.addEventListener('DOMContentLoaded', () => {
  loadUsers();
  // Refresh every 30 seconds
  setInterval(loadUsers, 30000);
});

async function loadUsers() {
  try {
    const response = await fetch('/admin/api/users');
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    users = await response.json();
    renderUsers();
    updateStats();
  } catch (error) {
    showError('Failed to load users: ' + error.message);
  }
}

function renderUsers() {
  const container = document.getElementById('table-content');

  if (users.length === 0) {
    container.innerHTML = `
      <div class="empty-state">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4.354a4 4 0 110 5.292M15 21H3v-1a6 6 0 0112 0v1zm0 0h6v-1a6 6 0 00-9-5.197M13 7a4 4 0 11-8 0 4 4 0 018 0z"/>
        </svg>
        <h3>No users yet</h3>
        <p>Add your first user through the onboarding flow.</p>
      </div>
    `;
    return;
  }

  container.innerHTML = `
    <table class="users-table">
      <thead>
        <tr>
          <th>Username</th>
          <th>Trakt Display Name</th>
          <th>Token Status</th>
          <th>Last Updated</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${users.map(user => `
          <tr>
            <td><strong>${escapeHtml(user.username)}</strong></td>
            <td>${user.trakt_display_name ? escapeHtml(user.trakt_display_name) : '<em style="color: #9ca3af;">Not set</em>'}</td>
            <td>
              <span class="status-indicator status-${user.status}">
                <span class="status-dot"></span>
                ${user.status === 'healthy' ? 'Healthy' : user.status === 'warning' ? 'Warning' : 'Expired'}
              </span>
            </td>
            <td>${formatDate(user.updated)}</td>
            <td>
              <div class="actions">
                <button class="btn btn-edit" onclick="editUser('${user.id}')">Edit</button>
                <button class="btn btn-delete" onclick="deleteUser('${user.id}')">Delete</button>
              </div>
            </td>
          </tr>
        `).join('')}
      </tbody>
    </table>
  `;
}

function updateStats() {
  const total = users.length;
  const healthy = users.filter(u => u.status === 'healthy').length;
  const warning = users.filter(u => u.status === 'warning').length;
  const expired = users.filter(u => u.status === 'expired').length;

  document.getElementById('stat-total').textContent = total;
  document.getElementById('stat-healthy').textContent = healthy;
  document.getElementById('stat-warning').textContent = warning;
  document.getElementById('stat-expired').textContent = expired;
}

function editUser(id) {
  const user = users.find(u => u.id === id);
  if (!user) return;

  document.getElementById('edit-user-id').value = user.id;
  document.getElementById('edit-username').value = user.username;
  document.getElementById('edit-display-name').value = user.trakt_display_name || '';

  document.getElementById('edit-modal').classList.add('active');
}

function closeEditModal() {
  document.getElementById('edit-modal').classList.remove('active');
}

async function saveUser() {
  const id = document.getElementById('edit-user-id').value;
  const username = document.getElementById('edit-username').value.trim();
  const displayName = document.getElementById('edit-display-name').value.trim();

  if (!username) {
    alert('Username is required');
    return;
  }

  try {
    const response = await fetch(`/admin/api/users/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username: username,
        trakt_display_name: displayName || null
      })
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }

    closeEditModal();
    await loadUsers();
    showSuccess('User updated successfully');
  } catch (error) {
    showError('Failed to update user: ' + error.message);
  }
}

function deleteUser(id) {
  const user = users.find(u => u.id === id);
  if (!user) return;

  document.getElementById('delete-user-id').value = user.id;
  document.getElementById('delete-user-name').textContent = user.username;

  document.getElementById('delete-modal').classList.add('active');
}

function closeDeleteModal() {
  document.getElementById('delete-modal').classList.remove('active');
}

async function confirmDelete() {
  const id = document.getElementById('delete-user-id').value;

  try {
    const response = await fetch(`/admin/api/users/${id}`, {
      method: 'DELETE'
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }

    closeDeleteModal();
    await loadUsers();
    showSuccess('User deleted successfully');
  } catch (error) {
    showError('Failed to delete user: ' + error.message);
  }
}

function showError(message) {
  const container = document.getElementById('error-container');
  container.innerHTML = `<div class="error-message">${escapeHtml(message)}</div>`;
  setTimeout(() => container.innerHTML = '', 5000);
}

function showSuccess(message) {
  const container = document.getElementById('error-container');
  container.innerHTML = `<div class="error-message" style="background: #d1fae5; color: #065f46;">${escapeHtml(message)}</div>`;
  setTimeout(() => container.innerHTML = '', 3000);
}

function formatTokenAge(hours) {
  if (hours < 1) return 'Less than 1 hour';
  if (hours < 24) return `${Math.floor(hours)} hours`;
  const days = Math.floor(hours / 24);
  return `${days} day${days > 1 ? 's' : ''}`;
}

// Close modals on escape key
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    closeEditModal();
    closeDeleteModal();
  }
});

// Close modals on background click
document.querySelectorAll('.modal').forEach(modal => {
  modal.addEventListener('click', (e) => {
    if (e.target === modal) {
      closeEditModal();
      closeDeleteModal();
    }
  });
});
