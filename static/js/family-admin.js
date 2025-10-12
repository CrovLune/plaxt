let familyGroups = [];
let currentGroupDetail = null;

document.addEventListener('DOMContentLoaded', () => {
  loadFamilyGroups();
  setInterval(loadFamilyGroups, 30000);
});

async function loadFamilyGroups() {
  try {
    const response = await fetch('/admin/api/family-groups');
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    familyGroups = await response.json();
    renderFamilyGroups();
    updateStats();
  } catch (error) {
    showError('Failed to load family groups: ' + error.message);
  }
}

function renderFamilyGroups() {
  const container = document.getElementById('table-content');

  if (familyGroups.length === 0) {
    container.innerHTML = `
      <div class="empty-state">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2M23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z"/>
        </svg>
        <h3>No family groups yet</h3>
        <p>Family groups will appear here after users complete the family onboarding flow.</p>
      </div>
    `;
    return;
  }

  container.innerHTML = `
    <table class="users-table">
      <thead>
        <tr>
          <th>Plex Username</th>
          <th>Members</th>
          <th>Authorized</th>
          <th>Webhook URL</th>
          <th>Created</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${familyGroups.map(group => `
          <tr>
            <td><strong>${escapeHtml(group.plex_username)}</strong></td>
            <td>${group.member_count}</td>
            <td>
              ${group.authorized_count === group.member_count
                ? `<span class="status-indicator status-healthy"><span class="status-dot"></span>${group.authorized_count}/${group.member_count}</span>`
                : group.authorized_count > 0
                ? `<span class="status-indicator status-warning"><span class="status-dot"></span>${group.authorized_count}/${group.member_count}</span>`
                : `<span class="status-indicator status-expired"><span class="status-dot"></span>${group.authorized_count}/${group.member_count}</span>`
              }
            </td>
            <td>
              <code style="font-size: 0.75rem; background: #f3f4f6; padding: 0.25rem 0.5rem; border-radius: 4px;">${group.webhook_url}</code>
            </td>
            <td>${formatDate(group.created_at)}</td>
            <td>
              <div class="actions">
                <button class="btn btn-edit" onclick="viewGroupDetail('${group.id}')">View</button>
                <button class="btn btn-delete" onclick="deleteGroup('${group.id}', '${escapeHtml(group.plex_username)}')">Delete</button>
              </div>
            </td>
          </tr>
        `).join('')}
      </tbody>
    </table>
  `;
}

function updateStats() {
  const total = familyGroups.length;
  const totalMembers = familyGroups.reduce((sum, g) => sum + g.member_count, 0);
  const authorizedMembers = familyGroups.reduce((sum, g) => sum + g.authorized_count, 0);
  const pendingMembers = totalMembers - authorizedMembers;

  document.getElementById('stat-total').textContent = total;
  document.getElementById('stat-members').textContent = totalMembers;
  document.getElementById('stat-authorized').textContent = authorizedMembers;
  document.getElementById('stat-pending').textContent = pendingMembers;
}

async function viewGroupDetail(groupId) {
  try {
    const response = await fetch(`/admin/api/family-groups/${groupId}`);
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    currentGroupDetail = await response.json();
    renderGroupDetail();
    document.getElementById('detail-modal').classList.add('active');
  } catch (error) {
    showError('Failed to load group details: ' + error.message);
  }
}

function renderGroupDetail() {
  if (!currentGroupDetail) return;

  const content = document.getElementById('detail-content');
  content.innerHTML = `
    <div style="margin-bottom: 1.5rem;">
      <h3 style="margin-bottom: 0.5rem;">Group Information</h3>
      <div style="display: grid; gap: 0.75rem;">
        <div>
          <strong>Plex Username:</strong> ${escapeHtml(currentGroupDetail.plex_username)}
        </div>
        <div>
          <strong>Webhook URL:</strong><br>
          <code style="font-size: 0.875rem; background: #f3f4f6; padding: 0.5rem; border-radius: 4px; display: inline-block; margin-top: 0.25rem; word-break: break-all;">${currentGroupDetail.webhook_url}</code>
        </div>
        <div>
          <strong>Created:</strong> ${formatDate(currentGroupDetail.created_at)}
        </div>
        <div>
          <strong>Last Updated:</strong> ${formatDate(currentGroupDetail.updated_at)}
        </div>
      </div>
    </div>

    <div style="margin-bottom: 1rem; display: flex; justify-content: space-between; align-items: center;">
      <h3 style="margin: 0;">Members (${currentGroupDetail.members.length})</h3>
      ${currentGroupDetail.members.length < 10
        ? `<button class="btn btn-edit" style="font-size: 0.875rem; padding: 0.5rem 1rem;" onclick="showAddMemberModal('${currentGroupDetail.id}')">+ Add Member</button>`
        : '<span style="color: #6b7280; font-size: 0.875rem;">Maximum members reached</span>'
      }
    </div>

    <table class="users-table">
      <thead>
        <tr>
          <th>Label</th>
          <th>Trakt Display Name</th>
          <th>Status</th>
          <th>Token Age</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${currentGroupDetail.members.map(member => `
          <tr>
            <td><strong>${escapeHtml(member.label)}</strong></td>
            <td>${member.trakt_display_name ? escapeHtml(member.trakt_display_name) : '<em style="color: #9ca3af;">Not set</em>'}</td>
            <td>
              <span class="status-indicator status-${member.status}">
                <span class="status-dot"></span>
                ${member.status === 'authorized' ? 'Authorized' : member.status === 'pending' ? 'Pending' : member.status === 'expired' ? 'Expired' : 'Failed'}
              </span>
            </td>
            <td>${member.token_age_hours !== null ? formatTokenAge(member.token_age_hours) : '-'}</td>
            <td>
              <button class="btn btn-delete" style="font-size: 0.875rem; padding: 0.4rem 0.8rem;" onclick="removeMember('${currentGroupDetail.id}', '${member.id}', '${escapeHtml(member.label)}')">Remove</button>
            </td>
          </tr>
        `).join('')}
      </tbody>
    </table>
  `;
}

function closeDetailModal() {
  document.getElementById('detail-modal').classList.remove('active');
  currentGroupDetail = null;
}

function deleteGroup(groupId, plexUsername) {
  document.getElementById('delete-group-id').value = groupId;
  document.getElementById('delete-group-name').textContent = plexUsername;
  document.getElementById('delete-modal').classList.add('active');
}

function closeDeleteModal() {
  document.getElementById('delete-modal').classList.remove('active');
}

async function confirmDeleteGroup() {
  const groupId = document.getElementById('delete-group-id').value;

  try {
    const response = await fetch(`/admin/api/family-groups/${groupId}`, {
      method: 'DELETE'
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }

    closeDeleteModal();
    await loadFamilyGroups();
    showSuccess('Family group deleted successfully');
  } catch (error) {
    showError('Failed to delete family group: ' + error.message);
  }
}

function showAddMemberModal(groupId) {
  document.getElementById('add-member-group-id').value = groupId;
  document.getElementById('add-member-label').value = '';
  document.getElementById('add-member-modal').classList.add('active');
}

function closeAddMemberModal() {
  document.getElementById('add-member-modal').classList.remove('active');
}

async function confirmAddMember() {
  const groupId = document.getElementById('add-member-group-id').value;
  const label = document.getElementById('add-member-label').value.trim();

  if (!label) {
    alert('Member label is required');
    return;
  }

  try {
    const response = await fetch(`/admin/api/family-groups/${groupId}/members`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ label })
    });

    if (!response.ok) {
      const errorData = await response.json().catch(() => ({}));
      throw new Error(errorData.error || `HTTP ${response.status}`);
    }

    closeAddMemberModal();
    await viewGroupDetail(groupId); // Refresh detail view
    await loadFamilyGroups(); // Refresh main list
    showSuccess('Member added successfully');
  } catch (error) {
    showError('Failed to add member: ' + error.message);
  }
}

function removeMember(groupId, memberId, memberLabel) {
  document.getElementById('remove-member-group-id').value = groupId;
  document.getElementById('remove-member-id').value = memberId;
  document.getElementById('remove-member-name').textContent = memberLabel;
  document.getElementById('remove-member-modal').classList.add('active');
}

function closeRemoveMemberModal() {
  document.getElementById('remove-member-modal').classList.remove('active');
}

async function confirmRemoveMember() {
  const groupId = document.getElementById('remove-member-group-id').value;
  const memberId = document.getElementById('remove-member-id').value;

  try {
    const response = await fetch(`/admin/api/family-groups/${groupId}/members/${memberId}`, {
      method: 'DELETE'
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }

    closeRemoveMemberModal();
    await viewGroupDetail(groupId); // Refresh detail view
    await loadFamilyGroups(); // Refresh main list
    showSuccess('Member removed successfully');
  } catch (error) {
    showError('Failed to remove member: ' + error.message);
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

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    closeDetailModal();
    closeDeleteModal();
    closeAddMemberModal();
    closeRemoveMemberModal();
  }
});

document.querySelectorAll('.modal').forEach(modal => {
  modal.addEventListener('click', (e) => {
    if (e.target === modal) {
      closeDetailModal();
      closeDeleteModal();
      closeAddMemberModal();
      closeRemoveMemberModal();
    }
  });
});
