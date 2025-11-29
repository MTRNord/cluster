#! /bin/bash

migrate() {
    python manage.py migrate "$@" || return 1
}

initdb() {
    python manage.py initdb "$@" || return 1
}

init() {
    echo "Running init function..."
    migrate || return 1
    migrate "django_celery_beat" || return 1
    initdb || return 1
    python manage.py compile_themes || return 1
    python manage.py collectstatic --no-input || return 1
    python manage.py admin_code || return 1
    return 0
}

update() {
    echo "Running update function..."
    migrate || return 1
    python manage.py compile_themes || return 1
    python manage.py collectstatic --no-input || return 1

    return 0
}

op="${1}"
if [[ "${op}" == "init" ]]; then
    init || exit 1
elif [[ "${op}" == "update" ]]; then
    update || exit 1
else
    echo "Unknown operation ${op}, aborting."
    exit 1
fi

exit 0
