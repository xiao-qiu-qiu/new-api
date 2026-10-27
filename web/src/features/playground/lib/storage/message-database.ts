/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
const DATABASE_NAME = 'new-api-playground'
const MESSAGE_STORE = 'messages'

function withMessageStore<T>(
  mode: IDBTransactionMode,
  operation: (store: IDBObjectStore) => IDBRequest<T>
): Promise<T> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DATABASE_NAME, 1)

    request.onupgradeneeded = () => {
      request.result.createObjectStore(MESSAGE_STORE)
    }
    request.onerror = () => reject(request.error)
    request.onsuccess = () => {
      const database = request.result
      database.onversionchange = () => database.close()

      try {
        const transaction = database.transaction(MESSAGE_STORE, mode)
        const result = operation(transaction.objectStore(MESSAGE_STORE))

        transaction.oncomplete = () => {
          database.close()
          resolve(result.result)
        }
        transaction.onabort = () => {
          database.close()
          reject(transaction.error ?? new Error('Message storage was aborted'))
        }
        transaction.onerror = () => {
          database.close()
          reject(transaction.error)
        }
      } catch (error) {
        database.close()
        reject(error)
      }
    }
  })
}

export function readMessageRecord(key: string): Promise<unknown> {
  return withMessageStore('readonly', (store) => store.get(key))
}

export function writeMessageRecord(
  key: string,
  value: unknown
): Promise<IDBValidKey> {
  return withMessageStore('readwrite', (store) => store.put(value, key))
}

export function deleteMessageRecord(key: string): Promise<undefined> {
  return withMessageStore('readwrite', (store) => store.delete(key))
}
